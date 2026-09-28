package server

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/metrics"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// sendMailRequest represents the JSON payload for sending mail from the web UI.
// sendMailRequest represents the JSON payload for sending mail from the web UI.
type sendMailRequest struct {
	From struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"from"`
	To          []string `json:"to"`
	Cc          []string `json:"cc,omitempty"`
	Bcc         []string `json:"bcc,omitempty"`
	Subject     string   `json:"subject"`
	Body        string   `json:"body"`
	ContentType string   `json:"contentType,omitempty"` // e.g. text/plain or text/html
	// Optional: account and mailbox indicate where to save the sent message. If empty,
	// the server will try to use the account's configured SentMailbox. If that is
	// also empty, no copy will be saved.
	Account string `json:"account,omitempty"`
	Mailbox string `json:"mailbox,omitempty"`
}

// sendMailHandler returns an http.HandlerFunc that accepts a sendMailRequest JSON and sends it via SMTP.
// attachment holds a parsed file attachment from a multipart form upload.
type attachment struct {
	Filename string
	Data     []byte
	MIMEType string
}

type inlineImage struct {
	CID      string
	Filename string
	Data     []byte
	MIMEType string
}

// isSpecialMailbox reports whether name is INBOX or the account's sent, draft,
// or deleted mailbox — places a reply copy should not be saved into.
func isSpecialMailbox(name string, acc *cache.Account) bool {
	if name == "" {
		return false
	}
	for _, special := range []string{"INBOX", acc.SentMailbox, acc.DraftMailbox, acc.DeletedMailbox} {
		if special != "" && strings.EqualFold(name, special) {
			return true
		}
	}
	return false
}

func sendMailHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		in, err := parseComposeRequest(w, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req := in.req

		if len(req.To) == 0 && len(req.Cc) == 0 && len(req.Bcc) == 0 {
			http.Error(w, "no recipients specified", http.StatusBadRequest)
			return
		}

		toHeader, err := sanitizeRecipients(req.To)
		if err != nil {
			http.Error(w, "invalid recipient address", http.StatusBadRequest)
			return
		}
		ccHeader, err := sanitizeRecipients(req.Cc)
		if err != nil {
			http.Error(w, "invalid Cc address", http.StatusBadRequest)
			return
		}

		msgBytes, msgID, bodyContentType := buildComposeMessage(in, toHeader, ccHeader)

		smtpHost, smtpPort, username, password, useTLS, startTLS, insecure := findSMTPConfigForFrom(store, req.From.Email)

		if smtpHost == "" {
			http.Error(w, "no SMTP configuration found for sender", http.StatusInternalServerError)
			return
		}

		// Build recipient list and deduplicate
		rcptMap := make(map[string]struct{})
		var recipients []string
		for _, r := range append(append(req.To, req.Cc...), req.Bcc...) {
			rr := strings.TrimSpace(r)
			if rr == "" {
				continue
			}
			if _, ok := rcptMap[rr]; !ok {
				rcptMap[rr] = struct{}{}
				recipients = append(recipients, rr)
			}
		}

		if err := sendMail(smtpHost, smtpPort, username, password, useTLS, startTLS, insecure, req.From.Email, recipients, msgBytes); err != nil {
			slog.Error("failed to send mail", "error", err)
			http.Error(w, "failed to send mail: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Log successful send for debugging
		slog.Info("mail sent", "from", req.From.Email, "to", req.To, "cc", req.Cc, "bcc", req.Bcc, "subject", req.Subject)

		if saveAccount := composeSaveAccount(store, req); saveAccount != nil {
			// The request mailbox wins unless it is a special one (INBOX, drafts, ...),
			// so replies land next to the original but never pollute those.
			targetMailbox := req.Mailbox
			if isSpecialMailbox(targetMailbox, saveAccount) {
				targetMailbox = ""
			}
			if targetMailbox == "" {
				targetMailbox = saveAccount.SentMailbox
			}
			if targetMailbox != "" {
				env := &cache.Envelope{
					Date:      time.Now(),
					Subject:   req.Subject,
					From:      []*cache.Address{envelopeAddress(req.From.Name, req.From.Email)},
					To:        envelopeAddresses(req.To),
					Cc:        envelopeAddresses(req.Cc),
					Bcc:       envelopeAddresses(req.Bcc),
					MessageID: msgID,
				}
				appendCopyAsync(store, saveAccount, targetMailbox, []string{`\Seen`}, msgBytes, env, req.Body, bodyContentType, "save sent copy")
			}
		}

		metrics.EmailsSent.WithLabelValues(req.From.Email, "success").Inc()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}
}

// saveDraftHandler saves a draft message into the account's Drafts mailbox without sending it.
func saveDraftHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		in, err := parseComposeRequest(w, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req := in.req

		toHeader, err := sanitizeRecipients(req.To)
		if err != nil {
			http.Error(w, "invalid recipient address", http.StatusBadRequest)
			return
		}
		ccHeader, err := sanitizeRecipients(req.Cc)
		if err != nil {
			http.Error(w, "invalid Cc address", http.StatusBadRequest)
			return
		}

		msgBytes, msgID, contentType := buildComposeMessage(in, toHeader, ccHeader)

		saveAccount := composeSaveAccount(store, req)

		if saveAccount == nil {
			http.Error(w, "no account found for draft", http.StatusBadRequest)
			return
		}

		targetMailbox := "Drafts"
		if saveAccount.DraftMailbox != "" {
			targetMailbox = saveAccount.DraftMailbox
		}

		env := &cache.Envelope{
			Date:      time.Now(),
			Subject:   req.Subject,
			From:      []*cache.Address{envelopeAddress(req.From.Name, req.From.Email)},
			To:        envelopeAddresses(req.To),
			Cc:        envelopeAddresses(req.Cc),
			MessageID: msgID,
		}
		foundUid, targetMailbox, err := appendCopy(store, saveAccount, targetMailbox, []string{`\Draft`}, msgBytes, env, req.Body, contentType)
		if err != nil {
			slog.Error("failed to save draft", "account", saveAccount.Name, "mailbox", targetMailbox, "error", err)
			http.Error(w, "failed to save draft: "+err.Error(), http.StatusInternalServerError)
			return
		}
		metrics.DraftsSaved.Inc()

		slog.Info("saved draft", "account", saveAccount.Name, "mailbox", targetMailbox, "uid", foundUid)
		w.Header().Set("Content-Type", "application/json")
		if foundUid > 0 {
			json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "uid": foundUid, "mailbox": targetMailbox})
		} else {
			json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "mailbox": targetMailbox})
		}
	}
}

// findSMTPConfigForFrom tries to pick SMTP settings from store.Accounts.
// Returns host, port, username, password, useTLS (implicit TLS), startTLS.
func findSMTPConfigForFrom(store *cache.Store, from string) (host string, port int, username, password string, useTLS, startTLS, insecure bool) {
	// Simple heuristic: look for account with name "bot" first, then any account with an SMTP-like server entry (contains "smtp")
	store.Mu.RLock()
	defer store.Mu.RUnlock()

	// extract email address if present (<user@host>) - not used further but left for future heuristics
	if strings.Contains(from, "<") && strings.Contains(from, ">") {
		// keep parsing available for future use
		_ = strings.TrimSpace(from[strings.Index(from, "<")+1 : strings.Index(from, ">")])
	}

	// Try to match the From against account FromAddrs and use that account's outgoing setting.
	// Accept From values like "Name <email@host>" or just "email@host".
	extractEmail := func(s string) string {
		s = strings.TrimSpace(s)
		if strings.Contains(s, "<") && strings.Contains(s, ">") {
			start := strings.Index(s, "<") + 1
			end := strings.Index(s, ">")
			if start < end {
				return strings.ToLower(strings.TrimSpace(s[start:end]))
			}
		}
		return strings.ToLower(s)
	}
	fromEmail := extractEmail(from)

	for i := range store.Accounts {
		acc := store.Accounts[i]
		// check explicit from addresses
		for _, fa := range acc.FromAddrs {
			if strings.ToLower(fa.Email) == fromEmail {
				// Prefer outgoing specified on the FromAddress itself
				if fa.Outgoing != "" {
					if out, ok := store.OutgoingServers[fa.Outgoing]; ok {
						host, port := parseHostPort(out.Server, out.Port)
						return host, port, out.Username, out.Password, out.TLS == "tls", out.TLS == "starttls", out.InsecureSkipVerify
					}
				}
				// then prefer account-level outgoing key
				if acc.Outgoing != "" {
					if out, ok := store.OutgoingServers[acc.Outgoing]; ok {
						host, port := parseHostPort(out.Server, out.Port)
						return host, port, out.Username, out.Password, out.TLS == "tls", out.TLS == "starttls", out.InsecureSkipVerify
					}
				}
				// otherwise, if the account itself looks like an SMTP server, use its outgoing server map fallback
				break
			}
		}
	}

	// Fallback: pick the first outgoing server available in the store
	for key, out := range store.OutgoingServers {
		if key != "" {
			host, port := parseHostPort(out.Server, out.Port)
			return host, port, out.Username, out.Password, out.TLS == "tls", out.TLS == "starttls", out.InsecureSkipVerify
		}
	}

	// No SMTP info
	return "", 0, "", "", false, false, false
}

func parseHostPort(server string, portStr string) (string, int) {
	host := server
	port := 0
	if p, err := strconv.Atoi(portStr); err == nil {
		port = p
	}
	if strings.Contains(server, ":") {
		h, p, err := net.SplitHostPort(server)
		if err == nil {
			host = h
			if pi, err := strconv.Atoi(p); err == nil {
				port = pi
			}
		}
	}
	return host, port
}

// sendMail sends the raw message bytes to the SMTP server.
func sendMail(host string, port int, username, password string, useTLS, startTLS, insecure bool, from string, to []string, msg []byte) error {
	addr := fmt.Sprintf("%s:%d", host, port)

	if useTLS {
		tlsconfig := smtpTLSConfig(host, insecure)
		conn, err := tls.Dial("tcp", addr, tlsconfig)
		if err != nil {
			return err
		}
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			return err
		}
		defer c.Close()

		if username != "" {
			auth := smtp.PlainAuth("", username, password, host)
			if err = c.Auth(auth); err != nil {
				return err
			}
		}

		if err = c.Mail(from); err != nil {
			return err
		}
		for _, rcpt := range to {
			if err = c.Rcpt(rcpt); err != nil {
				return err
			}
		}
		w, err := c.Data()
		if err != nil {
			return err
		}
		_, err = w.Write(msg)
		if err != nil {
			return err
		}
		err = w.Close()
		if err != nil {
			return err
		}
		return c.Quit()
	}

	c, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer c.Close()

	hostOnly := host
	if strings.Contains(host, ":") {
		h, _, _ := net.SplitHostPort(host)
		hostOnly = h
	}

	if err = c.Hello("localhost"); err != nil {
		return err
	}

	if startTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("STARTTLS required but not offered by %s; refusing to send credentials in cleartext", hostOnly)
		}
		if err = c.StartTLS(smtpTLSConfig(hostOnly, insecure)); err != nil {
			return err
		}
	}

	if username != "" {
		auth := smtp.PlainAuth("", username, password, hostOnly)
		if err = c.Auth(auth); err != nil {
			return err
		}
	}

	if err = c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err = c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	_, err = w.Write(msg)
	if err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
