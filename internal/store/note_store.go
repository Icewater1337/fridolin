package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Notes: messages the bridge sent for something it keeps track of, an
// errand's question say, whose text carries words that must not reach a
// brain's run (docs/BRAINS.md, "Errands"). Each message is kept by its
// WhatsApp id with what it is about (ref) and what a run is shown in its
// place (history), so that a reply quoting it is known by its ref and
// shows history instead of the words. Kept for NoteKeep.

// migrateV11 adds sent_notes.
func migrateV11(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE sent_notes (
			msg_id     TEXT PRIMARY KEY,           -- the WhatsApp message id
			chat_jid   TEXT NOT NULL,
			brain      TEXT NOT NULL DEFAULT '',   -- whose history records it
			ref        TEXT NOT NULL,              -- what it is about: "errand-question:<id>", say
			history    TEXT NOT NULL,              -- what a run is shown in its place
			created_at DATETIME NOT NULL
		)`,
		`CREATE INDEX sent_notes_by_time ON sent_notes (created_at)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// NoteKeep is how long a note is known by its id: a reply that quotes an
// older one shows its text as WhatsApp quotes it.
const NoteKeep = 60 * 24 * time.Hour

// Note is a message the bridge sent for Ref.
type Note struct {
	MsgID     string
	Chat      string
	Brain     string
	Ref       string
	History   string
	CreatedAt time.Time
}

// ErrNoNote is a message id the bridge kept no note of.
var ErrNoNote = errors.New("no such note")

// SaveNote keeps n, before its message goes out: a reply could quote it
// as soon as it arrived.
func (s *Store) SaveNote(ctx context.Context, n Note) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO sent_notes (msg_id, chat_jid, brain, ref, history, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		n.MsgID, n.Chat, n.Brain, n.Ref, n.History, n.CreatedAt.UTC())
	return err
}

// DeleteNote forgets the note of msgID, whose message did not go out
// after all.
func (s *Store) DeleteNote(ctx context.Context, msgID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sent_notes WHERE msg_id = ?`, msgID)
	return err
}

// NoteOf is the note kept of message msgID, ErrNoNote for none.
func (s *Store) NoteOf(ctx context.Context, msgID string) (Note, error) {
	var n Note
	err := s.db.QueryRowContext(ctx,
		`SELECT msg_id, chat_jid, brain, ref, history, created_at FROM sent_notes WHERE msg_id = ?`, msgID).
		Scan(&n.MsgID, &n.Chat, &n.Brain, &n.Ref, &n.History, &n.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Note{}, ErrNoNote
	}
	return n, err
}

// PruneNotes forgets the notes kept before before.
func (s *Store) PruneNotes(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sent_notes WHERE created_at < ?`, before.UTC())
	return err
}
