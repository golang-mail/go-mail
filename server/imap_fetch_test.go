package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/emersion/go-imap"

	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/testserver"
)

// startServerBackedStore runs the test server and returns a store whose only
// account points at it, with the given emails cached in INBOX.
func startServerBackedStore(t *testing.T, emails map[uint32]*cache.CachedEmail) (*testserver.TestServer, *cache.Store) {
	t.Helper()
	ts, err := testserver.Start(testserver.Config{Address: "127.0.0.1", Username: "u", Password: "p", TLS: "none", StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ts.Close() })
	store := newHandlerStore(emails)
	acc := store.Accounts[0]
	acc.Server, acc.Port, acc.Username, acc.Password, acc.TLS = "127.0.0.1", strconv.Itoa(ts.Port()), "u", "p", "none"
	return ts, store
}

func serverFlags(t *testing.T, acc *cache.Account, mailbox string, uid uint32) []string {
	t.Helper()
	c, err := acc.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()
	if _, err := c.Select(mailbox, true); err != nil {
		t.Fatal(err)
	}
	seq := new(imap.SeqSet)
	seq.AddNum(uid)
	ch := make(chan *imap.Message, 4)
	if err := c.UidFetch(seq, []imap.FetchItem{imap.FetchFlags}, ch); err != nil {
		t.Fatal(err)
	}
	m := <-ch
	if m == nil {
		t.Fatalf("uid %d missing on server", uid)
	}
	return m.Flags
}

const attachmentRaw = "From: a@x\r\nTo: u@x\r\nSubject: with file\r\nMIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
	"--b\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n" +
	"--b\r\nContent-Type: text/plain; name=\"notes.txt\"\r\nContent-Disposition: attachment; filename=\"notes.txt\"\r\n\r\nthe notes\r\n" +
	"--b--\r\n"

func TestAttachmentHandler_StreamsWithoutMarkingSeen(t *testing.T) {
	ts, store := startServerBackedStore(t, map[uint32]*cache.CachedEmail{})
	uid, err := ts.Deliver("u", "INBOX", []byte(attachmentRaw))
	if err != nil {
		t.Fatal(err)
	}

	w := get(attachmentHandler(store), "/api/attachment?account=acc&mailbox=INBOX&uid="+strconv.Itoa(int(uid))+"&attachment=notes.txt")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got := strings.TrimSpace(w.Body.String()); got != "the notes" {
		t.Fatalf("attachment body = %q", got)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename=notes.txt`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if flags := serverFlags(t, store.Accounts[0], "INBOX", uid); len(flags) != 0 {
		t.Fatalf("downloading an attachment changed server flags to %v", flags)
	}

	w = get(attachmentHandler(store), "/api/attachment?account=acc&mailbox=INBOX&uid="+strconv.Itoa(int(uid))+"&attachment=missing.txt")
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown attachment status %d", w.Code)
	}
}

func TestRSVPLinksHandler_FetchesOverIMAPWithoutMarkingSeen(t *testing.T) {
	ts, store := startServerBackedStore(t, map[uint32]*cache.CachedEmail{
		1: {UID: 1, ContentType: "text/calendar", Envelope: &cache.Envelope{Subject: "Invitation: Planning"}},
	})
	if uid, err := ts.Deliver("u", "INBOX", []byte(googleInviteRaw)); err != nil || uid != 1 {
		t.Fatalf("deliver = %d, %v", uid, err)
	}

	w := httptest.NewRecorder()
	rsvpLinksHandler(store)(w, httptest.NewRequest(http.MethodGet, "/api/mail/rsvp-links?account=acc&mailbox=INBOX&uid=1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var links map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &links); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(links["ACCEPTED"], "rst=1") || !strings.Contains(links["DECLINED"], "rst=2") || !strings.Contains(links["TENTATIVE"], "rst=3") {
		t.Fatalf("links = %v", links)
	}
	if flags := serverFlags(t, store.Accounts[0], "INBOX", 1); len(flags) != 0 {
		t.Fatalf("reading RSVP links changed server flags to %v", flags)
	}
}
