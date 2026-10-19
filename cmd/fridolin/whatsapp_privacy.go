package main

import "github.com/Icewater1337/fridolin/internal/logx"

import "go.mau.fi/whatsmeow/types"

// maskChatJID is chat, a queue's JID, for the log: a group's as the rest
// of the log names it, a person's masked as a sender is.
func maskChatJID(chat string) string {
	jid, err := types.ParseJID(chat)
	switch {
	case err != nil:
		return logx.MaskDigits(chat, 0)
	case jid.Server == types.GroupServer:
		return chat
	}
	return logx.MaskJID(jid.User, "", jid.Server)
}
