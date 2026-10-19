package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// The outbox table: replies on their way to WhatsApp (see Outbox). A row
// holds one reply as the messages it goes out as, how many of them went
// out already, and when to try the rest again. A delivered reply's row
// is deleted. One given up on keeps only what the dashboard says about
// it, not its text, for outboxFailedKeep.

// migrateV3 adds the outbox.
func migrateV3(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE outbox (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_jid    TEXT NOT NULL,
		brain       TEXT NOT NULL DEFAULT '', -- whose history records it once delivered
		history     TEXT NOT NULL DEFAULT '', -- what that history records; '' nothing
		parts       TEXT NOT NULL,            -- JSON array of outboxPart
		next_part   INTEGER NOT NULL DEFAULT 0,
		state       TEXT NOT NULL DEFAULT 'pending', -- or 'failed'
		attempts    INTEGER NOT NULL DEFAULT 0,
		created_at  DATETIME NOT NULL,
		next_try_at DATETIME NOT NULL,
		last_error  TEXT NOT NULL DEFAULT ''
	)`)
	return err
}

// outboxPart is one message of a reply: a text, or a poll.
type outboxPart struct {
	Text     string   `json:"text,omitempty"`
	Question string   `json:"question,omitempty"`
	Options  []string `json:"options,omitempty"`
	Max      int      `json:"max,omitempty"`
}

func (p outboxPart) isPoll() bool { return p.Question != "" }

// outboxRow is a reply waiting in the outbox.
type outboxRow struct {
	ID        int64
	Chat      string
	Brain     string
	History   string
	Parts     []outboxPart
	NextPart  int // the first part not sent yet
	Attempts  int
	CreatedAt time.Time
	NextTryAt time.Time
	LastError string
}

// AddOutbox puts a reply into the outbox, due now.
func (s *Store) AddOutbox(ctx context.Context, r outboxRow) (int64, error) {
	parts, err := json.Marshal(r.Parts)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO outbox (chat_jid, brain, history, parts, created_at, next_try_at) VALUES (?, ?, ?, ?, ?, ?)`,
		r.Chat, r.Brain, r.History, string(parts), r.CreatedAt.UTC(), r.CreatedAt.UTC())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// PendingOutbox returns the replies waiting, oldest first.
func (s *Store) PendingOutbox(ctx context.Context) ([]outboxRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, chat_jid, brain, history, parts, next_part, attempts, created_at, next_try_at, last_error
		FROM outbox WHERE state = 'pending' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		var parts string
		if err := rows.Scan(&r.ID, &r.Chat, &r.Brain, &r.History, &parts, &r.NextPart, &r.Attempts, &r.CreatedAt, &r.NextTryAt, &r.LastError); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(parts), &r.Parts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UndeliveredReplies returns the replies of brain that wait in the outbox,
// oldest first, with only what its history will record of them.
func (s *Store) UndeliveredReplies(ctx context.Context, brain string) ([]outboxRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, chat_jid, history, created_at
		FROM outbox WHERE state = 'pending' AND brain = ? AND history != '' ORDER BY id`, brain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		r := outboxRow{Brain: brain}
		if err := rows.Scan(&r.ID, &r.Chat, &r.History, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// OutboxSent records that the parts of reply id before next went out, so
// that a retry, after a restart too, goes on from there.
func (s *Store) OutboxSent(ctx context.Context, id int64, next int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE outbox SET next_part = ? WHERE id = ?`, next, id)
	return err
}

// OutboxRetry records a failed attempt and when to try again.
func (s *Store) OutboxRetry(ctx context.Context, id int64, attempts int, next time.Time, lastErr string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE outbox SET attempts = ?, next_try_at = ?, last_error = ? WHERE id = ?`,
		attempts, next.UTC(), lastErr, id)
	return err
}

// OutboxDelivered removes a reply that went out in full.
func (s *Store) OutboxDelivered(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM outbox WHERE id = ?`, id)
	return err
}

// OutboxGiveUp marks a reply as given up on, and drops its text.
func (s *Store) OutboxGiveUp(ctx context.Context, id int64, lastErr string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE outbox SET state = 'failed', parts = '[]', history = '', last_error = ?, next_try_at = ? WHERE id = ?`,
		lastErr, now.UTC(), id)
	return err
}

// outboxFailedKeep is how long the dashboard hears of a reply given up on.
const outboxFailedKeep = 7 * 24 * time.Hour

// outboxSummary is what the dashboard shows of the outbox.
type outboxSummary struct {
	Waiting       int       // replies not delivered yet
	OldestWaiting time.Time // when the oldest of them was written
	WaitingError  string    // why the last attempt failed, if one did
	Failed        int       // replies given up on lately
	LastFailed    time.Time
	FailedError   string
}

// PruneOutbox forgets the replies given up on before, which the dashboard
// no longer mentions.
func (s *Store) PruneOutbox(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM outbox WHERE state = 'failed' AND next_try_at < ?`, before.UTC())
	return err
}

// OutboxSummary counts the replies waiting and those given up on within
// outboxFailedKeep of now. It only reads, since the dashboard asks for it
// every few seconds; the outbox prunes older ones (PruneOutbox).
//
// Its queries read in one transaction, so that they see the outbox as it
// was at one moment: a reply given up on between two of them would
// otherwise count both as waiting and as given up on.
func (s *Store) OutboxSummary(ctx context.Context, now time.Time) (outboxSummary, error) {
	var sum outboxSummary
	since := now.Add(-outboxFailedKeep).UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return sum, err
	}
	defer tx.Rollback()
	// MIN and MAX would lose the column's type, which the driver reads
	// times by, so the rows themselves are read.
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE state = 'pending'`).Scan(&sum.Waiting); err != nil {
		return sum, err
	}
	err = tx.QueryRowContext(ctx, `SELECT created_at FROM outbox WHERE state = 'pending' ORDER BY id LIMIT 1`).Scan(&sum.OldestWaiting)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return sum, err
	}
	err = tx.QueryRowContext(ctx, `SELECT last_error FROM outbox WHERE state = 'pending' AND last_error != '' ORDER BY id DESC LIMIT 1`).Scan(&sum.WaitingError)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return sum, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE state = 'failed' AND next_try_at >= ?`, since).Scan(&sum.Failed); err != nil {
		return sum, err
	}
	err = tx.QueryRowContext(ctx, `SELECT next_try_at, last_error FROM outbox WHERE state = 'failed' AND next_try_at >= ?
		ORDER BY next_try_at DESC, id DESC LIMIT 1`, since).Scan(&sum.LastFailed, &sum.FailedError)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return sum, err
	}
	return sum, nil
}
