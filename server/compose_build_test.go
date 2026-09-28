package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/golang-mail/go-mail/cache"
)

func multipartCompose(t *testing.T, fields map[string]string, filename, content string) *http.Request {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	fw, err := mw.CreateFormFile("attachments", filename)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte(content))
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/mail/draft", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestParseComposeRequest_MultipartCarriesAttachments(t *testing.T) {
	req := multipartCompose(t, map[string]string{"from_email": "me@x", "to": "you@y", "subject": "hi", "body": "b"}, "notes.txt", "hello")
	in, err := parseComposeRequest(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatal(err)
	}
	if in.req.From.Email != "me@x" || len(in.req.To) != 1 || in.req.Subject != "hi" {
		t.Fatalf("fields = %+v", in.req)
	}
	if len(in.attachments) != 1 || in.attachments[0].Filename != "notes.txt" || string(in.attachments[0].Data) != "hello" {
		t.Fatalf("attachments = %+v", in.attachments)
	}
}

func TestParseComposeRequest_RemovesSpilledTempFiles(t *testing.T) {
	old := multipartMaxMemory
	multipartMaxMemory = 0 // force every file part onto disk
	t.Cleanup(func() { multipartMaxMemory = old })

	before := countMultipartTempFiles(t)
	req := multipartCompose(t, map[string]string{"from_email": "me@x"}, "big.bin", strings.Repeat("a", 1<<20))
	if _, err := parseComposeRequest(httptest.NewRecorder(), req); err != nil {
		t.Fatal(err)
	}
	if got := countMultipartTempFiles(t); got != before {
		t.Fatalf("multipart temp files leaked: %d before, %d after", before, got)
	}
}

func countMultipartTempFiles(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "multipart-") {
			n++
		}
	}
	return n
}

func TestParseComposeRequest_JSON(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"from": map[string]string{"email": "me@x"}, "to": []string{"you@y"}, "subject": "hi", "body": "b"})
	req := httptest.NewRequest(http.MethodPost, "/api/mail/send", bytes.NewReader(body))
	in, err := parseComposeRequest(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatal(err)
	}
	if in.req.From.Email != "me@x" || len(in.attachments) != 0 {
		t.Fatalf("got %+v", in)
	}
}

func TestBuildComposeMessage_WithAttachmentIsMultipartMixed(t *testing.T) {
	in := composeInput{
		req:         sendMailRequest{Subject: "hi", Body: "body text"},
		attachments: []attachment{{Filename: "notes.txt", MIMEType: "text/plain", Data: []byte("hello")}},
	}
	in.req.From.Email = "me@x"
	msg, msgID, ct := buildComposeMessage(in, []string{"you@y"}, nil)
	s := string(msg)
	for _, want := range []string{"Content-Type: multipart/mixed;", `filename="notes.txt"`, "Message-ID: " + msgID, "Subject: hi"} {
		if !strings.Contains(s, want) {
			t.Errorf("message missing %q:\n%s", want, s)
		}
	}
	if ct != "text/plain; charset=utf-8" {
		t.Fatalf("body content type = %q", ct)
	}
}

func TestBuildComposeMessage_PlainWithoutAttachments(t *testing.T) {
	in := composeInput{req: sendMailRequest{Subject: "hi", Body: "body text", ContentType: "text/html; charset=utf-8"}}
	msg, _, ct := buildComposeMessage(in, []string{"you@y"}, nil)
	if strings.Contains(string(msg), "multipart/") {
		t.Fatalf("plain message must not be multipart:\n%s", msg)
	}
	if ct != "text/html; charset=utf-8" {
		t.Fatalf("body content type = %q", ct)
	}
}

func TestMoveSelected_CreatesMissingDestination(t *testing.T) {
	store := newHandlerStore(map[uint32]*cache.CachedEmail{1: sampleFiltered()})
	body := `{"account":"acc","mailbox":"INBOX","to_mailbox":"Later","uids":[1]}`
	w := httptest.NewRecorder()
	moveSelectedHandler(store)(w, httptest.NewRequest(http.MethodPost, "/api/mails/move-selected", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d: %s", w.Code, w.Body.String())
	}
	store.Mu.RLock()
	defer store.Mu.RUnlock()
	_, later := findMailbox(store, "acc", "Later")
	if later == nil || later.Emails[1] == nil {
		t.Fatal("destination mailbox should be created and hold the moved mail")
	}
	if _, inbox := findMailbox(store, "acc", "INBOX"); inbox.Emails[1] != nil {
		t.Fatal("mail should be gone from the source")
	}
}

func TestEnvelopeAddresses_SplitMailboxAndHost(t *testing.T) {
	got := envelopeAddresses([]string{"you@y.example", "Some One <one@o.example>", " ", "not-an-address"})
	want := []*cache.Address{
		{MailboxName: "you", HostName: "y.example"},
		{PersonalName: "Some One", MailboxName: "one", HostName: "o.example"},
		{MailboxName: "not-an-address"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d addresses, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if *got[i] != *want[i] {
			t.Errorf("addr %d = %+v, want %+v", i, *got[i], *want[i])
		}
	}
}

func TestEnvelopeAddress_FromNameAndEmail(t *testing.T) {
	got := envelopeAddress("Me", "me@mine.example")
	want := cache.Address{PersonalName: "Me", MailboxName: "me", HostName: "mine.example"}
	if *got != want {
		t.Fatalf("got %+v, want %+v", *got, want)
	}
}
