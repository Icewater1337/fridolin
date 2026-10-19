package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/jsonx"

	"github.com/Icewater1337/fridolin/internal/chat"
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
	InboundInterrupted = "interrupted"
	// inboundQueued is a message waiting in a brain's queue. Its lines
	// are in that brain's history (Inbound.logged), and it keeps them.
	InboundQueued = "queued"
	// inboundHeld is a message the debouncer held, in no history yet.
	InboundHeld = "held"
	// inboundLate is a message that came in once the stop had begun.
	// Nothing has looked at it: it is handled as one that comes in then,
	// commands and the OTP guard included.
	InboundLate = "late"
	// inboundChange is an edit or a deletion that came in then.
	InboundChange = "change"
)

// keptInbound is a row of the inbound queue.
type KeptInbound struct {
	ID     int64
	Kind   string
	Chat   string
	Brain  string
	MsgID  string
	SentAt time.Time
	// Msg is the message of a queued, held or late row, Change the change
	// of a change row.
	Msg    brainruntime.Inbound
	Change chat.Change
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
func KeepInbound(ctx context.Context, s *store.Store, kind, brain string, m brainruntime.Inbound) error {
	body, err := json.Marshal(m.Save())
	if err != nil {
		return err
	}
	return s.KeepInbound(ctx, store.InboundRow{Kind: kind, Chat: m.JID, Brain: brain, MsgID: m.ID, Body: string(body), SentAt: m.At})
}

// keepChange keeps c, an edit or a deletion that came in at at, for the
// next start.
func KeepChange(ctx context.Context, s *store.Store, c chat.Change, at time.Time) error {
	body, err := json.Marshal(storedChange(c))
	if err != nil {
		return err
	}
	return s.KeepInbound(ctx, store.InboundRow{Kind: InboundChange, Chat: c.Chat, MsgID: c.Target, Body: string(body), SentAt: at})
}

// keepInterrupted keeps that the stop cut short brain's run for m.
func KeepInterrupted(ctx context.Context, s *store.Store, brain string, m brainruntime.Inbound) error {
	return s.KeepInbound(ctx, store.InboundRow{Kind: InboundInterrupted, Chat: m.JID, Brain: brain, MsgID: m.ID, SentAt: m.At})
}

// takeInbound decodes rows inside the store transaction, before deletion.
// Malformed rows are reported and skipped; the remaining rows keep their order.
func TakeInbound(ctx context.Context, s *store.Store) ([]KeptInbound, error) {
	var out []KeptInbound
	var bad []error
	err := s.TakeInbound(ctx, func(row store.InboundRow) {
		k := KeptInbound{ID: row.ID, Kind: row.Kind, Chat: row.Chat, Brain: row.Brain, MsgID: row.MsgID, SentAt: row.SentAt}
		body := row.Body

		switch k.Kind {
		case InboundQueued, InboundHeld, InboundLate:
			var m brainruntime.SavedInbound
			if err := DecodeSavedInbound([]byte(body), &m); err != nil {
				bad = append(bad, fmt.Errorf("row %d: %w", k.ID, err))
				return
			}
			k.Msg = m.Restore()
		case InboundChange:
			var c storedChange
			if err := decodeStoredChange([]byte(body), &c); err != nil {
				bad = append(bad, fmt.Errorf("row %d: %w", k.ID, err))
				return
			}
			k.Change = chat.Change(c)
		case InboundInterrupted:
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

// decodeSavedInbound preserves the persisted queue's decoder diagnostics when
// its representation moves to the runtime package. The underlying typed error
// remains available to callers through errors.As.
func DecodeSavedInbound(body []byte, out *brainruntime.SavedInbound) error {
	return jsonx.RenameTypeError(json.Unmarshal(body, out), func(t reflect.Type) string {
		if t == reflect.TypeFor[brainruntime.SavedInbound]() {
			return "main.storedInbound"
		}
		return ""
	}, func(name string) string {
		if name == "SavedInbound" {
			return "storedInbound"
		}
		return ""
	})
}

// decodeStoredChange keeps persisted-row diagnostics stable across the package move.
func decodeStoredChange(body []byte, out *storedChange) error {
	return jsonx.RenameTypeError(json.Unmarshal(body, out), func(t reflect.Type) string {
		if t == reflect.TypeFor[storedChange]() {
			return "main.storedChange"
		}
		return ""
	}, nil)
}
