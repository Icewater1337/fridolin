package main

import (
	"github.com/Icewater1337/fridolin/internal/access"

	"go.mau.fi/whatsmeow/types"
)

// stripMentions removes the @-mentions of the given accounts from text and
// trims the result. A mention is "@" and the account's user part, and must
// not be the start of a longer number.
func stripMentions(text string, jids []types.JID) string {
	users := make([]string, len(jids))
	for i, jid := range jids {
		users[i] = jid.User
	}
	return access.StripMentions(text, users)
}
