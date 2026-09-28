package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	netmail "net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/golang-mail/go-mail/cache"
)

// icalProp is a property value with its parameters kept verbatim
// (e.g. "TZID=Europe/Zagreb" or "VALUE=DATE") so the reply echoes them.
type icalProp struct {
	Params string
	Value  string
}

func (p icalProp) line(name string) string {
	if p.Params != "" {
		return name + ";" + p.Params + ":" + p.Value
	}
	return name + ":" + p.Value
}

// inviteEvent is the subset of a VEVENT needed to answer it.
type inviteEvent struct {
	UID           string
	Sequence      string
	Organizer     string
	OrganizerName string
	DTStart       icalProp
	DTEnd         icalProp
	RecurrenceID  icalProp
	Summary       string
	Attendees     []string // lower-cased addresses
}

var rsvpPartstats = map[string]string{
	"ACCEPTED":  "Accepted",
	"TENTATIVE": "Tentative",
	"DECLINED":  "Declined",
}

// rsvpSend delivers the reply and rsvpSaveCopy files it in Sent; tests swap them out.
var rsvpSend = func(store *cache.Store, from string, to []string, msg []byte) error {
	host, port, username, password, useTLS, startTLS, insecure := findSMTPConfigForFrom(store, from)
	if host == "" {
		return errors.New("no SMTP configuration found for sender")
	}
	return sendMail(host, port, username, password, useTLS, startTLS, insecure, from, to, msg)
}

var rsvpSaveCopy = func(store *cache.Store, acc *cache.Account, mailbox string, msg []byte, env *cache.Envelope) {
	appendCopyAsync(store, acc, mailbox, []string{`\Seen`}, msg, env, env.Subject, "text/plain", "save RSVP copy")
}

var veventRE = regexp.MustCompile(`(?is)BEGIN:VEVENT(.*?)END:VEVENT`)

// unfoldICal joins continuation lines (CRLF or LF followed by whitespace).
func unfoldICal(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n ", "")
	return strings.ReplaceAll(s, "\n\t", "")
}

func unescapeICal(s string) string {
	r := strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`)
	return r.Replace(s)
}

func escapeICal(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`)
	return r.Replace(s)
}

// splitICalLine returns NAME, params (as written) and value of "NAME;P=V:value".
func splitICalLine(line string) (name string, params []string, value string) {
	// The first ':' outside a quoted parameter value ends the name/params.
	inQuote := false
	for i, c := range line {
		switch {
		case c == '"':
			inQuote = !inQuote
		case c == ':' && !inQuote:
			head := line[:i]
			value = line[i+1:]
			parts := strings.Split(head, ";")
			return strings.ToUpper(parts[0]), parts[1:], value
		}
	}
	return strings.ToUpper(line), nil, ""
}

func icalParam(params []string, key string) string {
	for _, p := range params {
		if k, v, ok := strings.Cut(p, "="); ok && strings.EqualFold(k, key) {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

func mailtoAddress(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(strings.ToLower(v), "mailto:") {
		v = v[len("mailto:"):]
	}
	return strings.ToLower(strings.TrimSpace(v))
}

func parseInvite(body string) (*inviteEvent, error) {
	m := veventRE.FindStringSubmatch(unfoldICal(body))
	if m == nil {
		return nil, errors.New("no VEVENT in message")
	}
	ev := &inviteEvent{Sequence: "0"}
	for line := range strings.SplitSeq(m[1], "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		name, params, value := splitICalLine(line)
		switch name {
		case "UID":
			ev.UID = strings.TrimSpace(value)
		case "SEQUENCE":
			ev.Sequence = strings.TrimSpace(value)
		case "SUMMARY":
			ev.Summary = unescapeICal(strings.TrimSpace(value))
		case "DTSTART":
			ev.DTStart = icalProp{Params: strings.Join(params, ";"), Value: strings.TrimSpace(value)}
		case "DTEND":
			ev.DTEnd = icalProp{Params: strings.Join(params, ";"), Value: strings.TrimSpace(value)}
		case "RECURRENCE-ID":
			ev.RecurrenceID = icalProp{Params: strings.Join(params, ";"), Value: strings.TrimSpace(value)}
		case "ORGANIZER":
			ev.Organizer = mailtoAddress(value)
			ev.OrganizerName = icalParam(params, "CN")
		case "ATTENDEE":
			if a := mailtoAddress(value); a != "" {
				ev.Attendees = append(ev.Attendees, a)
			}
		}
	}
	if ev.UID == "" {
		return nil, errors.New("invite has no UID")
	}
	if ev.Organizer == "" {
		return nil, errors.New("invite has no organizer")
	}
	return ev, nil
}

// pickRSVPAttendee chooses which of our identities answers the invite.
func pickRSVPAttendee(acc *cache.Account, attendees []string) (addr, name string) {
	for _, fa := range acc.FromAddrs {
		for _, a := range attendees {
			if strings.EqualFold(fa.Email, a) {
				return strings.ToLower(fa.Email), fa.Name
			}
		}
	}
	if len(acc.FromAddrs) > 0 && acc.FromAddrs[0].Email != "" {
		return strings.ToLower(acc.FromAddrs[0].Email), acc.FromAddrs[0].Name
	}
	if strings.Contains(acc.Username, "@") {
		return strings.ToLower(acc.Username), ""
	}
	return "", ""
}

func buildRSVPReply(ev *inviteEvent, attendee, attendeeName, partstat string, now time.Time) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\r\n", a...) }
	w("BEGIN:VCALENDAR")
	w("VERSION:2.0")
	w("PRODID:-//go-mail//EN")
	w("METHOD:REPLY")
	w("BEGIN:VEVENT")
	w("UID:%s", ev.UID)
	w("SEQUENCE:%s", ev.Sequence)
	w("DTSTAMP:%s", now.UTC().Format("20060102T150405Z"))
	if ev.OrganizerName != "" {
		w("ORGANIZER;CN=%s:mailto:%s", ev.OrganizerName, ev.Organizer)
	} else {
		w("ORGANIZER:mailto:%s", ev.Organizer)
	}
	if attendeeName != "" {
		w("ATTENDEE;CN=%s;PARTSTAT=%s:mailto:%s", attendeeName, partstat, attendee)
	} else {
		w("ATTENDEE;PARTSTAT=%s:mailto:%s", partstat, attendee)
	}
	if ev.RecurrenceID.Value != "" {
		w("%s", ev.RecurrenceID.line("RECURRENCE-ID"))
	}
	if ev.DTStart.Value != "" {
		w("%s", ev.DTStart.line("DTSTART"))
	}
	if ev.DTEnd.Value != "" {
		w("%s", ev.DTEnd.line("DTEND"))
	}
	if ev.Summary != "" {
		w("SUMMARY:%s", escapeICal(ev.Summary))
	}
	w("END:VEVENT")
	w("END:VCALENDAR")
	return b.String()
}

func rsvpSubject(ev *inviteEvent, partstat string) string {
	return rsvpPartstats[partstat] + ": " + ev.Summary
}

func buildRSVPMessage(ev *inviteEvent, from netmail.Address, partstat string, now time.Time) (msg []byte, msgID string) {
	subject := rsvpSubject(ev, partstat)
	to := netmail.Address{Name: ev.OrganizerName, Address: ev.Organizer}
	boundary := "rsvp-" + uuid.New().String()
	msgID = fmt.Sprintf("<%s@go-mail>", uuid.New().String())

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\n", from.String())
	fmt.Fprintf(&buf, "To: %s\r\n", to.String())
	fmt.Fprintf(&buf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&buf, "Message-ID: %s\r\n", msgID)
	fmt.Fprintf(&buf, "Date: %s\r\n", now.Format(time.RFC1123Z))
	buf.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", boundary)

	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	buf.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	fmt.Fprintf(&buf, "%s\r\n\r\n", subject)

	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	buf.WriteString("Content-Type: text/calendar; charset=UTF-8; method=REPLY\r\n\r\n")
	buf.WriteString(buildRSVPReply(ev, from.Address, from.Name, partstat, now))
	fmt.Fprintf(&buf, "\r\n--%s--\r\n", boundary)
	return buf.Bytes(), msgID
}

func rsvpHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		partstat := strings.ToUpper(q.Get("response"))
		if _, ok := rsvpPartstats[partstat]; !ok {
			http.Error(w, "response must be ACCEPTED, TENTATIVE or DECLINED", http.StatusBadRequest)
			return
		}
		uid64, err := strconv.ParseUint(q.Get("uid"), 10, 32)
		if err != nil {
			http.Error(w, "invalid uid", http.StatusBadRequest)
			return
		}

		store.Mu.RLock()
		acc, _, email := findEmail(store, q.Get("account"), q.Get("mailbox"), uint32(uid64))
		var body, contentType string
		if email != nil {
			body, contentType = email.Body, email.ContentType
		}
		store.Mu.RUnlock()
		if email == nil {
			http.Error(w, "Email not found", http.StatusNotFound)
			return
		}
		if !strings.HasPrefix(contentType, "text/calendar") {
			http.Error(w, "not a calendar invite", http.StatusUnprocessableEntity)
			return
		}
		ev, err := parseInvite(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		attendee, attendeeName := pickRSVPAttendee(acc, ev.Attendees)
		if attendee == "" {
			http.Error(w, "no from address configured for this account", http.StatusUnprocessableEntity)
			return
		}

		now := time.Now()
		from := netmail.Address{Name: attendeeName, Address: attendee}
		msg, msgID := buildRSVPMessage(ev, from, partstat, now)
		if err := rsvpSend(store, attendee, []string{ev.Organizer}, msg); err != nil {
			slog.Error("failed to send RSVP", "organizer", ev.Organizer, "error", err)
			http.Error(w, "failed to send RSVP: "+err.Error(), http.StatusBadGateway)
			return
		}
		if acc.SentMailbox != "" {
			rsvpSaveCopy(store, acc, acc.SentMailbox, msg, &cache.Envelope{
				Date:      now,
				Subject:   rsvpSubject(ev, partstat),
				From:      []*cache.Address{envelopeAddress(attendeeName, attendee)},
				To:        []*cache.Address{envelopeAddress(ev.OrganizerName, ev.Organizer)},
				MessageID: msgID,
			})
		}
		slog.Info("RSVP sent", "from", attendee, "organizer", ev.Organizer, "response", partstat)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "sent", "organizer": ev.Organizer, "response": partstat})
	}
}
