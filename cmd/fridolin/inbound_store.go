package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/store"
)

// The inbound_queue table: what came in from WhatsApp that no brain had
// started on when the bridge stopped, and the runs the stop cut short.
// WhatsApp took those messages as delivered, so nothing would bring them
// back: the stop keeps them here (Bridge.Stop), and the next start takes
// them up and deletes them (Bridge.resume). Messages from elsewhere are
// not kept: whoever handed them in hears that they were dropped
// (Inbound.Done), or can hand them in again.

// The kinds of row. The next start takes them up in this order: the
// notices for the runs cut short, what the brains had taken, what the
// debouncer held, then what arrived once the stop had begun, in the order
// it came in.
const (
	// inboundInterrupted is a message whose run the stop cut short. Only
	// its chat, brain and ID are kept, for the notice its chat gets.
	inboundInterrupted = "interrupted"
	// inboundQueued is a message waiting in a brain's queue. Its lines
	// are in that brain's history (Inbound.logged), and it keeps them.
	inboundQueued = "queued"
	// inboundHeld is a message the debouncer held, in no history yet.
	inboundHeld = "held"
	// inboundLate is a message that came in once the stop had begun.
	// Nothing has looked at it: it is handled as one that comes in then,
	// commands and the OTP guard included.
	inboundLate = "late"
	// inboundChange is an edit or a deletion that came in then.
	inboundChange = "change"
)

// keptInbound is a row of the inbound queue.
type keptInbound struct {
	ID     int64
	Kind   string
	Chat   string
	Brain  string
	MsgID  string
	SentAt time.Time
	// Msg is the message of a queued, held or late row, Change the change
	// of a change row.
	Msg    Inbound
	Change Change
}

// storedInbound is an Inbound as the inbound queue keeps it: all of it
// but Done, which a message from WhatsApp does not have.
type storedInbound struct {
	JID     string          `json:"jid"`
	ChatAlt string          `json:"chat_alt,omitempty"`
	Sender  string          `json:"sender"`
	Text    string          `json:"text,omitempty"`
	Media   []string        `json:"media,omitempty"`
	Brain   string          `json:"brain,omitempty"`
	Quoted  *Quote          `json:"quoted,omitempty"`
	Silent  bool            `json:"silent,omitempty"`
	Source  string          `json:"source"`
	At      time.Time       `json:"at"`
	ID      string          `json:"id,omitempty"`
	Logged  *history.Entry  `json:"logged,omitempty"`
	Parts   []storedInbound `json:"parts,omitempty"`
}

func storeInbound(m Inbound) storedInbound {
	s := storedInbound{
		JID: m.JID, ChatAlt: m.ChatAlt, Sender: m.Sender, Text: m.Text, Media: m.Media, Brain: m.Brain,
		Quoted: m.Quoted, Silent: m.Silent, Source: m.Source, At: m.At, ID: m.ID,
	}
	if m.logged != (history.Entry{}) {
		logged := m.logged
		s.Logged = &logged
	}
	for _, p := range m.parts {
		s.Parts = append(s.Parts, storeInbound(p))
	}
	return s
}

func (s storedInbound) inbound() Inbound {
	m := Inbound{InboundContent: InboundContent{JID: s.JID, ChatAlt: s.ChatAlt, Sender: s.Sender, Text: s.Text, Media: s.Media, Brain: s.Brain,
		Quoted: s.Quoted, Silent: s.Silent, Source: s.Source, At: s.At, ID: s.ID},
	}
	if s.Logged != nil {
		m.logged = *s.Logged
	}
	for _, p := range s.Parts {
		m.parts = append(m.parts, p.inbound())
	}
	return m
}

// storedChange is a messageChange as the inbound queue keeps it.
type storedChange struct {
	Kind    string `json:"kind"`
	Chat    string `json:"chat"`
	ChatAlt string `json:"chat_alt,omitempty"`
	Sender  string `json:"sender"`
	Target  string `json:"target"`
	Text    string `json:"text,omitempty"`
}

// keepInbound keeps m, a message of kind inboundQueued, inboundHeld or
// inboundLate that brain had ("" for none), for the next start.
func keepInbound(ctx context.Context, s *store.Store, kind, brain string, m Inbound) error {
	body, err := json.Marshal(storeInbound(m))
	if err != nil {
		return err
	}
	return s.KeepInbound(ctx, store.InboundRow{Kind: kind, Chat: m.JID, Brain: brain, MsgID: m.ID, Body: string(body), SentAt: m.At})
}

// keepChange keeps c, an edit or a deletion that came in at at, for the
// next start.
func keepChange(ctx context.Context, s *store.Store, c Change, at time.Time) error {
	body, err := json.Marshal(storedChange(c))
	if err != nil {
		return err
	}
	return s.KeepInbound(ctx, store.InboundRow{Kind: inboundChange, Chat: c.Chat, MsgID: c.Target, Body: string(body), SentAt: at})
}

// keepInterrupted keeps that the stop cut short brain's run for m.
func keepInterrupted(ctx context.Context, s *store.Store, brain string, m Inbound) error {
	return s.KeepInbound(ctx, store.InboundRow{Kind: inboundInterrupted, Chat: m.JID, Brain: brain, MsgID: m.ID, SentAt: m.At})
}

// takeInbound decodes rows inside the store transaction, before deletion.
// Malformed rows are reported and skipped; the remaining rows keep their order.
func takeInbound(ctx context.Context, s *store.Store) ([]keptInbound, error) {
	var out []keptInbound
	var bad []error
	err := s.TakeInbound(ctx, func(row store.InboundRow) {
		k := keptInbound{ID: row.ID, Kind: row.Kind, Chat: row.Chat, Brain: row.Brain, MsgID: row.MsgID, SentAt: row.SentAt}
		body := row.Body

		switch k.Kind {
		case inboundQueued, inboundHeld, inboundLate:
			var m storedInbound
			if err := json.Unmarshal([]byte(body), &m); err != nil {
				bad = append(bad, fmt.Errorf("row %d: %w", k.ID, err))
				return
			}
			k.Msg = m.inbound()
		case inboundChange:
			var c storedChange
			if err := json.Unmarshal([]byte(body), &c); err != nil {
				bad = append(bad, fmt.Errorf("row %d: %w", k.ID, err))
				return
			}
			k.Change = Change(c)
		case inboundInterrupted:
		default:
			bad = append(bad, fmt.Errorf("row %d: unknown kind %q", k.ID, k.Kind))
			return
		}
		out = append(out, k)
	})
	if err != nil {
		return nil, err
	}
	return out, errors.Join(bad...)
}
