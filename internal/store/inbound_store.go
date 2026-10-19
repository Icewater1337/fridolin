package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// InboundRow is one persisted inbound message or change. Body is opaque JSON;
// its message, history and runtime types belong to the caller.
type InboundRow struct {
	ID     int64
	Kind   string
	Chat   string
	Brain  string
	MsgID  string
	Body   string
	SentAt time.Time
}

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

// KeepInbound stores a row for the next start. Zero sent time means now.
func (s *Store) KeepInbound(ctx context.Context, row InboundRow) error {
	kind, chat, brain, msgID, body, sent := row.Kind, row.Chat, row.Brain, row.MsgID, row.Body, row.SentAt
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

// TakeInbound visits then deletes all rows in one transaction, in replay
// order: interrupted runs, queued messages, held messages, then late events.
// The caller decodes Body during the visit, before the queue is deleted.
// It must discard visited rows when TakeInbound returns an error.
func (s *Store) TakeInbound(ctx context.Context, visit func(InboundRow)) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Acquire the write reservation before reading. Upgrading a read snapshot
	// after another connection commits can fail immediately with SQLITE_BUSY,
	// even with a busy timeout, leaving startup recovery unperformed.
	if _, err := tx.ExecContext(ctx, `DELETE FROM inbound_queue WHERE 0`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, kind, chat_jid, brain, msg_id, body, sent_at FROM inbound_queue
		ORDER BY CASE kind WHEN 'interrupted' THEN 0 WHEN 'queued' THEN 1 WHEN 'held' THEN 2 ELSE 3 END, id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k InboundRow
		if err := rows.Scan(&k.ID, &k.Kind, &k.Chat, &k.Brain, &k.MsgID, &k.Body, &k.SentAt); err != nil {
			rows.Close()
			return err
		}
		visit(k)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM inbound_queue`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}
