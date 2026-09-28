package main

import (
	"strings"

	"github.com/golang-mail/go-mail/cache"
)

// requiredMailboxes lists the special mailboxes an account must have on the server.
func requiredMailboxes(acc *cache.Account) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range []string{acc.SentMailbox, acc.DraftMailbox, acc.DeletedMailbox} {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// resolveServerMailbox maps a configured mailbox name to the name the server
// actually has, tolerating case differences and an INBOX/ namespace prefix.
// existing is keyed by lower-cased server name.
func resolveServerMailbox(existing map[string]string, name string) (string, bool) {
	lower := strings.ToLower(name)
	if actual, ok := existing[lower]; ok {
		return actual, true
	}
	if actual, ok := existing["inbox/"+lower]; ok {
		return actual, true
	}
	return name, false
}
