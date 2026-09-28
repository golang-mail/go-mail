package server

import (
	"errors"
	"fmt"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-message"
	"github.com/golang-mail/go-mail/cache"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
)

var errAttachmentNotFound = errors.New("attachment not found")

// attachmentFromMessage finds the named part, descending nested multiparts.
func attachmentFromMessage(msg *imap.Message, name string) (*message.Entity, error) {
	if msg == nil || len(msg.Body) == 0 {
		return nil, errors.New("message has no body")
	}
	for _, literal := range msg.Body {
		if literal == nil {
			continue
		}
		entity, err := message.Read(literal)
		if err != nil && !message.IsUnknownCharset(err) {
			return nil, fmt.Errorf("read message: %w", err)
		}
		if p := findAttachmentPart(entity, name); p != nil {
			return p, nil
		}
	}
	return nil, errAttachmentNotFound
}

func findAttachmentPart(e *message.Entity, name string) *message.Entity {
	mr := e.MultipartReader()
	if mr == nil {
		if partFilename(e) == name {
			return e
		}
		return nil
	}
	for {
		p, err := mr.NextPart()
		if err != nil {
			return nil
		}
		if found := findAttachmentPart(p, name); found != nil {
			return found
		}
	}
}

func partFilename(e *message.Entity) string {
	if _, params, err := e.Header.ContentType(); err == nil && params["name"] != "" {
		return params["name"]
	}
	if _, params, err := e.Header.ContentDisposition(); err == nil {
		return params["filename"]
	}
	return ""
}

func attachmentHandler(store *cache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accountName := r.URL.Query().Get("account")
		mailboxName := r.URL.Query().Get("mailbox")
		uidStr := r.URL.Query().Get("uid")
		attachmentName := r.URL.Query().Get("attachment")

		uid, err := strconv.ParseUint(uidStr, 10, 32)
		if err != nil {
			http.Error(w, "invalid uid", http.StatusBadRequest)
			return
		}

		// Resolve the account under the lock, then release it: IMAP I/O and
		// streaming to the client must not stall every writer.
		store.Mu.RLock()
		acc := findAccount(store, accountName)
		store.Mu.RUnlock()
		if acc == nil {
			http.Error(w, "Account not found", http.StatusNotFound)
			return
		}
		slog.Info("downloading attachment", "name", attachmentName, "account", accountName, "mailbox", mailboxName, "uid", uid)

		c, err := acc.Acquire()
		if err != nil {
			http.Error(w, "Failed to connect to IMAP server", http.StatusInternalServerError)
			return
		}
		defer acc.Release(c)

		if _, err := c.Select(mailboxName, true); err != nil {
			http.Error(w, "Failed to select mailbox", http.StatusInternalServerError)
			return
		}

		seqset := new(imap.SeqSet)
		seqset.AddNum(uint32(uid))

		// PEEK so downloading never flips \Seen behind the cache's back; the
		// channel is drained in case the server pushes extra FETCH responses.
		messages := make(chan *imap.Message, 1)
		fetchDone := make(chan error, 1)
		go func() {
			fetchDone <- c.UidFetch(seqset, []imap.FetchItem{"BODY.PEEK[]"}, messages)
		}()

		msg := firstMessage(messages)
		if err := <-fetchDone; err != nil {
			http.Error(w, "Failed to fetch message", http.StatusInternalServerError)
			return
		}

		part, err := attachmentFromMessage(msg, attachmentName)
		if err != nil {
			if errors.Is(err, errAttachmentNotFound) || msg == nil {
				http.Error(w, "Attachment not found", http.StatusNotFound)
				return
			}
			http.Error(w, "Failed to read message", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": attachmentName}))
		w.Header().Set("Content-Type", part.Header.Get("Content-Type"))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if _, err := io.Copy(w, part.Body); err != nil {
			slog.Error("failed to stream attachment", "error", err)
		}
	}
}

// firstMessage returns the first FETCH response and discards the rest, so a
// server that also pushes a flag update cannot stall the command.
func firstMessage(ch <-chan *imap.Message) *imap.Message {
	var first *imap.Message
	for m := range ch {
		if first == nil {
			first = m
		}
	}
	return first
}
