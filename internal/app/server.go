package app

import (
	"context"
	"fmt"

	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/route"

	"go.mau.fi/whatsmeow/types"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
)

// maySend refuses a /send that who may not make: a brain may send only to
// its own chats (see brainChat), a caller without a token only to the
// allowed chats and only while script_api.tokenless_send is on. The
// scripts token may send anywhere checkSendJID lets through.
func (b *App) maySend(who chat.Principal, jid string, cfg *config.Settings) error {
	switch who.Kind {
	case chat.CallerScript:
		return nil
	case chat.CallerBrain:
		br := b.brainMgr.Get(who.Brain)
		if br == nil || !b.brainChat(br, jid, cfg) {
			return fmt.Errorf("brain %s may send only to its own chats, and %s is not one of them", who.Brain, jid)
		}
		return nil
	case chat.CallerAnonymous:
		if !cfg.ScriptAPI.TokenlessSend {
			return fmt.Errorf("%w (script_api.tokenless_send is off)", errTokenRequired)
		}
		if !allowedChat(jid, cfg) {
			return fmt.Errorf("without a token, /send reaches only the allowed chats (allowed_numbers, allowed_groups), and %s is not one", jid)
		}
		return nil
	}
	return fmt.Errorf("unknown caller kind %d", who.Kind)
}

// brainChat reports whether jid is one of br's own chats: its reply chat
// (reply_chat, or the one replyChatFor derives), or an allowed chat that
// routes to it: a group it claims, for the fallback brain an allowed group
// no brain claims, or, for the brain that gets DMs, an allowed number's DM.
func (b *App) brainChat(br *brain.Brain, jid string, cfg *config.Settings) bool {
	if jid == b.replyChatFor(br, cfg) {
		return true
	}
	return allowedChat(jid, cfg) && b.brainMgr.BrainFor(jid, chat.IsGroupJID(jid)) == br
}

// allowedChat reports whether jid is a chat config allows by name: a group
// in allowed_groups, or the chat of a number in allowed_numbers. allow_any_dm
// does not count: it lets anyone write in, not the bridge write to anyone.
func allowedChat(jid string, cfg *config.Settings) bool {
	if chat.IsGroupJID(jid) {
		return slices.Contains(cfg.AllowedGroups, jid)
	}
	return strings.HasSuffix(jid, "@"+types.DefaultUserServer) && slices.Contains(cfg.AllowedNumbers, chat.NormalizeJID(jid))
}

// appendHTTPMessageToHistory records a message who sent through /send in the
// history of whichever brain owns the target chat, marked with how it came
// in: the brain must not take a line a script or another brain sent for its
// own words. request is the id of the code request it makes, "" for none.
func (b *App) appendHTTPMessageToHistory(who chat.Principal, jid, text, request string) {
	br := b.brainMgr.BrainFor(jid, chat.IsGroupJID(jid))
	if br == nil {
		return
	}
	from := "script"
	if who.Kind == chat.CallerBrain {
		from = who.Brain
	}
	br.AppendEntry(history.Entry{From: from, Chat: jid, Text: text, Type: history.Out, Via: who.Via(), OTPRequest: request})
}

// handOff feeds files that arrived outside WhatsApp to the brain picked for
// them, for /ingest and the Plaud integration. The message names the brain,
// so it runs there even when jid, the chat that gets the answer, belongs to
// another brain.
func (b *App) handOff(br *brain.Brain, jid, sender, text string, media []string) {
	m := brain.Inbound{Inbound: chat.Inbound{JID: jid, Brain: br.ID(), Sender: sender, Text: text, Media: media, Source: chat.SourceIngest, At: time.Now()}}
	b.handleInbound(context.Background(), m)
}

// submitHandOff is handOff for a source that must know whether the brain
// took the files, as Plaud must: a recording cannot be sent again by
// whoever sent it. It skips the debouncer, which keeps messages only in
// memory, and queues the message at once: an error means the brain did
// not take it (its queue is full, or it is gone). done then hears how the
// run ended, see Inbound.Done.
func (b *App) submitHandOff(br *brain.Brain, jid, sender, text string, media []string, done func(error)) error {
	m := brain.Inbound{Inbound: chat.Inbound{JID: jid, Brain: br.ID(), Sender: sender, Text: text, Media: media, Source: chat.SourceIngest, At: time.Now(), Done: done}}
	_, _, _, err := b.brainMgr.Submit(m)
	return err
}

// replyChatFor picks the chat a brain answers in when a message did not come
// from a chat: its configured reply_chat, else your own DM (ownerChat) for
// the brain that gets DMs (the DM brain, or the fallback when there is
// none), else the brain's first group. The DM comes before any group: files
// handed in privately must not land in a group because the brain also
// serves one.
func (b *App) replyChatFor(br *brain.Brain, cfg *config.Settings) string {
	return replyChatForBrain(b.brainMgr.Manager, br, ownerChat(cfg))
}

// replyChatForBrain keeps runtime identity in reply selection, including a
// retained brain whose ID has since been reused by a different runtime.
func replyChatForBrain(mgr *brain.Manager, br *brain.Brain, ownerDM string) string {
	m := br.Manifest()
	if m.ReplyChat != "" {
		return m.ReplyChat
	}
	getsDMs := m.Routing.HandlesDMs || mgr.DMBrain() == br
	return route.ReplyChat(m, getsDMs, ownerDM)
}
