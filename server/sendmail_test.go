package server

import (
	"strings"
	"testing"

	"github.com/golang-mail/go-mail/testserver"
)

func startSMTP(t *testing.T, mode string) *testserver.TestServer {
	t.Helper()
	ts, err := testserver.Start(testserver.Config{Address: "127.0.0.1", SMTP: true, Username: "u", Password: "p", TLS: mode, StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ts.Close() })
	return ts
}

const outgoingRaw = "From: u@x\r\nTo: friend@x\r\nSubject: out\r\n\r\nhello\r\n"

func TestSendMail_PlainDeliversAndRecords(t *testing.T) {
	ts := startSMTP(t, "none")
	if err := sendMail("127.0.0.1", ts.SMTPPort(), "u", "p", false, false, false, "u@x", []string{"friend@x"}, []byte(outgoingRaw)); err != nil {
		t.Fatal(err)
	}
	got := ts.Delivered()
	if len(got) != 1 || got[0].From != "u@x" || len(got[0].Recipients) != 1 || got[0].Recipients[0] != "friend@x" {
		t.Fatalf("delivered = %+v", got)
	}
	if !strings.Contains(string(got[0].Raw), "Subject: out") {
		t.Fatalf("raw = %q", got[0].Raw)
	}
}

func TestSendMail_BadCredentialsFail(t *testing.T) {
	ts := startSMTP(t, "none")
	err := sendMail("127.0.0.1", ts.SMTPPort(), "u", "wrong", false, false, false, "u@x", []string{"friend@x"}, []byte(outgoingRaw))
	if err == nil || !strings.Contains(err.Error(), "535") {
		t.Fatalf("want a 535 auth failure, got %v", err)
	}
	if len(ts.Delivered()) != 0 {
		t.Fatal("nothing must be delivered after a failed AUTH")
	}
}

func TestSendMail_StartTLSRequiredButNotOffered(t *testing.T) {
	ts := startSMTP(t, "none")
	err := sendMail("127.0.0.1", ts.SMTPPort(), "u", "p", false, true, true, "u@x", []string{"friend@x"}, []byte(outgoingRaw))
	if err == nil || !strings.Contains(err.Error(), "STARTTLS required") {
		t.Fatalf("want the STARTTLS refusal, got %v", err)
	}
	if len(ts.Delivered()) != 0 {
		t.Fatal("credentials must not be sent in clear text")
	}
}

func TestSendMail_StartTLS(t *testing.T) {
	ts := startSMTP(t, "starttls")
	if err := sendMail("127.0.0.1", ts.SMTPPort(), "u", "p", false, true, false, "u@x", []string{"friend@x"}, []byte(outgoingRaw)); err == nil {
		t.Fatal("self-signed certificate must be rejected without insecure_skip_verify")
	}
	if err := sendMail("127.0.0.1", ts.SMTPPort(), "u", "p", false, true, true, "u@x", []string{"friend@x"}, []byte(outgoingRaw)); err != nil {
		t.Fatal(err)
	}
	if len(ts.Delivered()) != 1 {
		t.Fatalf("delivered = %d, want 1", len(ts.Delivered()))
	}
}

func TestSendMail_ImplicitTLS(t *testing.T) {
	ts := startSMTP(t, "tls")
	if err := sendMail("127.0.0.1", ts.SMTPPort(), "u", "p", true, false, false, "u@x", []string{"friend@x"}, []byte(outgoingRaw)); err == nil {
		t.Fatal("self-signed certificate must be rejected without insecure_skip_verify")
	}
	if err := sendMail("127.0.0.1", ts.SMTPPort(), "u", "p", true, false, true, "u@x", []string{"friend@x"}, []byte(outgoingRaw)); err != nil {
		t.Fatal(err)
	}
	if len(ts.Delivered()) != 1 {
		t.Fatalf("delivered = %d, want 1", len(ts.Delivered()))
	}
}
