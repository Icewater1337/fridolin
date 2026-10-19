package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
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

// migrateV4 adds the inbound queue.
func migrateV4(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE inbound_queue (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		kind       TEXT NOT NULL,            -- one of the inbound* kinds
		chat_jid   TEXT NOT NULL,
		brain      TEXT NOT NULL DEFAULT '', -- the brain that had it, '' for none yet
		msg_id     TEXT NOT NULL DEFAULT '', -- the WhatsApp message's ID
		body       TEXT NOT NULL DEFAULT '', -- the message or the change, as JSON
		sent_at    DATETIME NOT NULL,        -- when it was sent
		created_at DATETIME NOT NULL
	)`)
	return err
}

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
	Change messageChange
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
	Logged  *historyEntry   `json:"logged,omitempty"`
	Parts   []storedInbound `json:"parts,omitempty"`
}

func storeInbound(m Inbound) storedInbound {
	s := storedInbound{
		JID: m.JID, ChatAlt: m.ChatAlt, Sender: m.Sender, Text: m.Text, Media: m.Media, Brain: m.Brain,
		Quoted: m.Quoted, Silent: m.Silent, Source: m.Source, At: m.At, ID: m.ID,
	}
	if m.logged != (historyEntry{}) {
		logged := m.logged
		s.Logged = &logged
	}
	for _, p := range m.parts {
		s.Parts = append(s.Parts, storeInbound(p))
	}
	return s
}

func (s storedInbound) inbound() Inbound {
	m := Inbound{
		JID: s.JID, ChatAlt: s.ChatAlt, Sender: s.Sender, Text: s.Text, Media: s.Media, Brain: s.Brain,
		Quoted: s.Quoted, Silent: s.Silent, Source: s.Source, At: s.At, ID: s.ID,
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

// KeepInbound keeps m, a message of kind inboundQueued, inboundHeld or
// inboundLate that brain had ("" for none), for the next start.
func (s *Store) KeepInbound(ctx context.Context, kind, brain string, m Inbound) error {
	body, err := json.Marshal(storeInbound(m))
	if err != nil {
		return err
	}
	return s.keepInbound(ctx, kind, m.JID, brain, m.ID, string(body), m.At)
}

// KeepChange keeps c, an edit or a deletion that came in at at, for the
// next start.
func (s *Store) KeepChange(ctx context.Context, c messageChange, at time.Time) error {
	body, err := json.Marshal(storedChange(c))
	if err != nil {
		return err
	}
	return s.keepInbound(ctx, inboundChange, c.Chat, "", c.Target, string(body), at)
}

// KeepInterrupted keeps that the stop cut short brain's run for m.
func (s *Store) KeepInterrupted(ctx context.Context, brain string, m Inbound) error {
	return s.keepInbound(ctx, inboundInterrupted, m.JID, brain, m.ID, "", m.At)
}

func (s *Store) keepInbound(ctx context.Context, kind, chat, brain, msgID, body string, sent time.Time) error {
	now := time.Now().UTC()
	if sent.IsZero() {
		sent = now
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO inbound_queue (kind, chat_jid, brain, msg_id, body, sent_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		kind, chat, brain, msgID, body, sent.UTC(), now)
	if err != nil {
		return fmt.Errorf("keeping a message for %s for the next start: %w", chat, err)
	}
	return nil
}

// TakeInbound returns what the inbound queue holds, in the order to take
// it up in (see the kinds), and empties it, in one transaction: what it
// returns is gone from bridge.db. A row it cannot read is left out, and
// named in the error it returns alongside the others.
func (s *Store) TakeInbound(ctx context.Context) ([]keptInbound, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id, kind, chat_jid, brain, msg_id, body, sent_at FROM inbound_queue
		ORDER BY CASE kind WHEN 'interrupted' THEN 0 WHEN 'queued' THEN 1 WHEN 'held' THEN 2 ELSE 3 END, id`)
	if err != nil {
		return nil, err
	}
	var out []keptInbound
	var bad []error
	for rows.Next() {
		var k keptInbound
		var body string
		if err := rows.Scan(&k.ID, &k.Kind, &k.Chat, &k.Brain, &k.MsgID, &body, &k.SentAt); err != nil {
			rows.Close()
			return nil, err
		}
		switch k.Kind {
		case inboundQueued, inboundHeld, inboundLate:
			var m storedInbound
			if err := json.Unmarshal([]byte(body), &m); err != nil {
				bad = append(bad, fmt.Errorf("row %d: %w", k.ID, err))
				continue
			}
			k.Msg = m.inbound()
		case inboundChange:
			var c storedChange
			if err := json.Unmarshal([]byte(body), &c); err != nil {
				bad = append(bad, fmt.Errorf("row %d: %w", k.ID, err))
				continue
			}
			k.Change = messageChange(c)
		case inboundInterrupted:
		default:
			bad = append(bad, fmt.Errorf("row %d: unknown kind %q", k.ID, k.Kind))
			continue
		}
		out = append(out, k)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM inbound_queue`); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, errors.Join(bad...)
}
