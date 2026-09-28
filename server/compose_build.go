package server

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"mime"
	gomime "mime/multipart"
	"mime/quotedprintable"
	"net/http"
	netmail "net/mail"
	"net/textproto"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/golang-mail/go-mail/cache"
)

// composeInput is a parsed send or draft request.
type composeInput struct {
	req          sendMailRequest
	attachments  []attachment
	inlineImages []inlineImage
}

// multipartMaxMemory caps how much of a form stays in RAM before the rest
// spills to temp files; tests drop it to force spilling.
var multipartMaxMemory = int64(32 << 20)

// parseComposeRequest accepts JSON or multipart/form-data (with attachments
// and inline images) for both sending and saving drafts.
func parseComposeRequest(w http.ResponseWriter, r *http.Request) (composeInput, error) {
	var in composeInput
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := decodeJSONBody(w, r, &in.req); err != nil {
			return in, fmt.Errorf("invalid json: %w", err)
		}
		return in, nil
	}
	if err := r.ParseMultipartForm(multipartMaxMemory); err != nil {
		return in, fmt.Errorf("failed to parse form: %w", err)
	}
	// Every part is read into memory below; drop spill files right away.
	defer r.MultipartForm.RemoveAll()
	in.req.From.Name = r.FormValue("from_name")
	in.req.From.Email = r.FormValue("from_email")
	in.req.To = r.Form["to"]
	in.req.Cc = r.Form["cc"]
	in.req.Bcc = r.Form["bcc"]
	in.req.Subject = r.FormValue("subject")
	in.req.Body = r.FormValue("body")
	in.req.ContentType = r.FormValue("contentType")
	in.req.Account = r.FormValue("account")
	in.req.Mailbox = r.FormValue("mailbox")

	for _, fh := range r.MultipartForm.File["attachments"] {
		data, mtype, ok := readFormFile(fh, "application/octet-stream")
		if !ok {
			continue
		}
		in.attachments = append(in.attachments, attachment{Filename: fh.Filename, Data: data, MIMEType: mtype})
	}
	cids := r.Form["inline_cids"]
	for i, fh := range r.MultipartForm.File["inline_images"] {
		data, mtype, ok := readFormFile(fh, "image/png")
		if !ok {
			continue
		}
		cid := fh.Filename
		if i < len(cids) {
			cid = cids[i]
		}
		in.inlineImages = append(in.inlineImages, inlineImage{CID: cid, Filename: fh.Filename, Data: data, MIMEType: mtype})
	}
	return in, nil
}

func readFormFile(fh *gomime.FileHeader, defaultType string) ([]byte, string, bool) {
	f, err := fh.Open()
	if err != nil {
		slog.Error("failed to open uploaded file", "filename", fh.Filename, "error", err)
		return nil, "", false
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		slog.Error("failed to read uploaded file", "filename", fh.Filename, "error", err)
		return nil, "", false
	}
	mtype := fh.Header.Get("Content-Type")
	if mtype == "" {
		mtype = defaultType
	}
	return data, mtype, true
}

// buildComposeMessage renders the RFC 5322 message. It returns the raw bytes,
// the generated Message-ID and the body part's content type.
func buildComposeMessage(in composeInput, toHeader, ccHeader []string) (msg []byte, msgID, bodyContentType string) {
	req := in.req
	var buf bytes.Buffer
	fromAddr := netmail.Address{Name: req.From.Name, Address: req.From.Email}
	fmt.Fprintf(&buf, "From: %s\r\n", fromAddr.String())
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(toHeader, ", "))
	if len(ccHeader) > 0 {
		fmt.Fprintf(&buf, "Cc: %s\r\n", strings.Join(ccHeader, ", "))
	}
	fmt.Fprintf(&buf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", req.Subject))
	msgID = fmt.Sprintf("<%s@%s>", uuid.New().String(), "go-mail")
	fmt.Fprintf(&buf, "Message-ID: %s\r\n", msgID)
	fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	bodyContentType = "text/plain; charset=utf-8"
	if req.ContentType != "" {
		bodyContentType = req.ContentType
	}
	buf.WriteString("MIME-Version: 1.0\r\n")

	writeBody := func(w io.Writer) {
		qpw := quotedprintable.NewWriter(w)
		qpw.Write([]byte(req.Body))
		qpw.Close()
	}
	writeRelatedParts := func(relW *gomime.Writer) {
		bodyHeader := make(textproto.MIMEHeader)
		bodyHeader.Set("Content-Type", bodyContentType)
		bodyHeader.Set("Content-Transfer-Encoding", "quoted-printable")
		bodyPart, _ := relW.CreatePart(bodyHeader)
		writeBody(bodyPart)
		for _, img := range in.inlineImages {
			imgHeader := make(textproto.MIMEHeader)
			imgHeader.Set("Content-Type", img.MIMEType)
			imgHeader.Set("Content-Transfer-Encoding", "base64")
			imgHeader.Set("Content-ID", "<"+img.CID+">")
			imgHeader.Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", img.Filename))
			part, _ := relW.CreatePart(imgHeader)
			writeBase64(part, img.Data)
		}
		relW.Close()
	}

	switch {
	case len(in.attachments) > 0:
		mpw := gomime.NewWriter(&buf)
		fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", mpw.Boundary())
		if len(in.inlineImages) > 0 {
			relW := gomime.NewWriter(io.Discard)
			boundary := relW.Boundary()
			relHeader := make(textproto.MIMEHeader)
			relHeader.Set("Content-Type", "multipart/related; boundary="+boundary)
			relPart, _ := mpw.CreatePart(relHeader)
			relW = gomime.NewWriter(relPart)
			relW.SetBoundary(boundary)
			writeRelatedParts(relW)
		} else {
			bodyHeader := make(textproto.MIMEHeader)
			bodyHeader.Set("Content-Type", bodyContentType)
			bodyHeader.Set("Content-Transfer-Encoding", "quoted-printable")
			bodyPart, _ := mpw.CreatePart(bodyHeader)
			writeBody(bodyPart)
		}
		for _, att := range in.attachments {
			attHeader := make(textproto.MIMEHeader)
			attHeader.Set("Content-Type", att.MIMEType+"; name=\""+att.Filename+"\"")
			attHeader.Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", att.Filename))
			attHeader.Set("Content-Transfer-Encoding", "base64")
			part, _ := mpw.CreatePart(attHeader)
			writeBase64(part, att.Data)
		}
		mpw.Close()
	case len(in.inlineImages) > 0:
		relW := gomime.NewWriter(&buf)
		fmt.Fprintf(&buf, "Content-Type: multipart/related; boundary=%s\r\n\r\n", relW.Boundary())
		writeRelatedParts(relW)
	default:
		fmt.Fprintf(&buf, "Content-Type: %s\r\n", bodyContentType)
		buf.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		writeBody(&buf)
	}
	return buf.Bytes(), msgID, bodyContentType
}

func writeBase64(w io.Writer, data []byte) {
	encoded := base64.StdEncoding.EncodeToString(data)
	for i := 0; i < len(encoded); i += 76 {
		end := min(i+76, len(encoded))
		w.Write([]byte(encoded[i:end] + "\r\n"))
	}
}

// composeSaveAccount picks the account whose mailboxes receive a copy:
// the explicit request account, else the one owning the From address.
func composeSaveAccount(store *cache.Store, req sendMailRequest) *cache.Account {
	store.Mu.RLock()
	defer store.Mu.RUnlock()
	if req.Account != "" {
		if acc := findAccount(store, req.Account); acc != nil {
			return acc
		}
	}
	fromLower := strings.ToLower(req.From.Email)
	for _, acc := range store.Accounts {
		for _, fa := range acc.FromAddrs {
			if strings.ToLower(fa.Email) == fromLower {
				return acc
			}
		}
	}
	return nil
}

// envelopeAddresses converts compose recipients into cache addresses. The
// frontend rebuilds "mailbox@host" from the parts, so both halves are needed.
func envelopeAddresses(list []string) []*cache.Address {
	out := make([]*cache.Address, 0, len(list))
	for _, raw := range list {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		name, email := "", s
		if parsed, err := netmail.ParseAddress(s); err == nil {
			name, email = parsed.Name, parsed.Address
		}
		out = append(out, envelopeAddress(name, email))
	}
	return out
}

// envelopeAddress splits email at its last "@"; an address without one keeps
// the whole string as the mailbox so it still renders.
func envelopeAddress(name, email string) *cache.Address {
	addr := &cache.Address{PersonalName: name, MailboxName: email}
	if at := strings.LastIndex(email, "@"); at > 0 {
		addr.MailboxName, addr.HostName = email[:at], email[at+1:]
	}
	return addr
}
