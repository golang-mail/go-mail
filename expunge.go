package main

import "strings"

// mailboxesToExpunge lists mailboxes touched by permanent_delete. A plain
// delete only sets \Deleted so the mail stays recoverable until expunged.
func mailboxesToExpunge(actions []pendingAction) map[string]bool {
	out := make(map[string]bool)
	for _, pa := range actions {
		if strings.EqualFold(pa.action.Type, "permanent_delete") {
			out[pa.mailbox] = true
		}
	}
	return out
}
