package cache

import "testing"

type stubBackend struct{ emails map[uint32]*CachedEmail }

func (s stubBackend) LoadMailboxes(string) (map[string]*Mailbox, error) {
	return map[string]*Mailbox{"INBOX": {Name: "INBOX"}}, nil
}
func (s stubBackend) LoadEmails(string, string) (map[uint32]*CachedEmail, error) {
	return s.emails, nil
}
func (stubBackend) SaveMailboxes(string, map[string]*Mailbox) error          { return nil }
func (stubBackend) SaveEmails(string, string, map[uint32]*CachedEmail) error { return nil }
func (s stubBackend) SaveEmailsWithHint(_, _ string, emails map[uint32]*CachedEmail, _ []uint32) error {
	return s.SaveEmails("", "", emails)
}
func (stubBackend) ResetFiltered(string) error { return nil }
func (stubBackend) Close() error               { return nil }

func TestLoadCache_BackfillsNilEnvelope(t *testing.T) {
	s := NewStore()
	s.Backend = stubBackend{emails: map[uint32]*CachedEmail{1: {UID: 1}}}
	s.Accounts = []*Account{{Name: "acc"}}
	s.LoadCache()
	if e := s.Accounts[0].Mailboxes["INBOX"].Emails[1]; e.Envelope == nil {
		t.Fatal("want envelope backfilled on load")
	}
}
