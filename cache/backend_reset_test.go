package cache

import (
	"testing"
)

func filteredEmails() map[uint32]*CachedEmail {
	return map[uint32]*CachedEmail{
		1: {UID: 1, Envelope: &Envelope{Subject: "a"}, Labels: []string{"keep"}, Filtered: true},
		2: {UID: 2, Envelope: &Envelope{Subject: "b"}, Filtered: true},
	}
}

func TestResetFiltered_PersistentBackends(t *testing.T) {
	backends := map[string]func(dir string) CacheBackend{
		"sqlite": func(dir string) CacheBackend { return NewSQLiteBackend(dir) },
		"yaml":   func(dir string) CacheBackend { return NewYAMLBackend(dir) },
	}
	for name, mk := range backends {
		t.Run(name, func(t *testing.T) {
			b := mk(t.TempDir())
			defer b.Close()
			for _, mbox := range []string{"INBOX", "Archive"} {
				if err := b.SaveEmails("acc", mbox, filteredEmails()); err != nil {
					t.Fatal(err)
				}
			}
			if err := b.SaveEmails("other", "INBOX", filteredEmails()); err != nil {
				t.Fatal(err)
			}

			if err := b.ResetFiltered("acc"); err != nil {
				t.Fatal(err)
			}

			for _, mbox := range []string{"INBOX", "Archive"} {
				emails, err := b.LoadEmails("acc", mbox)
				if err != nil {
					t.Fatal(err)
				}
				for uid, e := range emails {
					if e.Filtered {
						t.Errorf("%s uid %d still filtered", mbox, uid)
					}
				}
				if emails[1].Labels[0] != "keep" {
					t.Errorf("%s: reset must not touch labels", mbox)
				}
			}
			other, _ := b.LoadEmails("other", "INBOX")
			if !other[1].Filtered {
				t.Error("another account must be untouched")
			}
		})
	}
}

func TestResetFiltered_MemorySidecar(t *testing.T) {
	m := NewMemoryBackend(t.TempDir())
	defer m.Close()
	if err := m.SaveEmails("acc", "INBOX", filteredEmails()); err != nil {
		t.Fatal(err)
	}
	if err := m.ResetFiltered("acc"); err != nil {
		t.Fatal(err)
	}
	fresh := map[uint32]*CachedEmail{1: {UID: 1}, 2: {UID: 2}}
	m.ApplySidecar("acc", "INBOX", fresh)
	if fresh[1].Filtered || fresh[2].Filtered {
		t.Fatal("sidecar must no longer mark emails filtered")
	}
	if len(fresh[1].Labels) != 1 || fresh[1].Labels[0] != "keep" {
		t.Fatal("reset must keep labels in the sidecar")
	}
}
