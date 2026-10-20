package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// The fride_threads table: coding work the core brain handed to FrIDE on a
// node (docs/NODES.md, "FrIDE"). A row ties fridolin's thread number, which
// the chat and the core brain's tools use, to the chat that started it and
// to where the work is now: the node, FrIDE's own thread there and the
// task it went to. A move changes the node and the task; the number stays.

// migrateV6 adds fride_threads (after M5's policy tables, migrateV5).
func migrateV6(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE fride_threads (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_jid     TEXT NOT NULL,             -- where its news goes
			brain        TEXT NOT NULL DEFAULT '',  -- whose history records it
			message      TEXT NOT NULL,             -- what was handed over
			repos        TEXT NOT NULL DEFAULT '[]',
			want_node    TEXT NOT NULL DEFAULT '',  -- a node id, name or tag it must run on
			node         TEXT NOT NULL DEFAULT '',  -- '' while it waits for one
			fride_thread INTEGER NOT NULL DEFAULT 0, -- FrIDE's thread on node, 0 for none
			task_id      TEXT NOT NULL DEFAULT '',
			branch       TEXT NOT NULL DEFAULT '',
			state        TEXT NOT NULL DEFAULT 'queued', -- queued, sent, closed
			fride_status TEXT NOT NULL DEFAULT '',  -- the thread's status as FrIDE last said
			archive      TEXT NOT NULL DEFAULT '',  -- the closed workspace's archive in the vault
			rationale    TEXT NOT NULL DEFAULT '',  -- the rationale document FrIDE wrote at the close
			created_at   DATETIME NOT NULL,
			updated_at   DATETIME NOT NULL
		)`,
		`CREATE INDEX fride_threads_by_node ON fride_threads (node, fride_thread)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// The states of a fride thread.
const (
	FrideQueued = "queued"
	FrideSent   = "sent"
	FrideClosed = "closed"
)

// FrideThread is one row of fride_threads.
type FrideThread struct {
	ID          int64
	Chat        string
	Brain       string
	Message     string
	Repos       []string
	WantNode    string
	Node        string
	FrideThread int
	TaskID      string
	Branch      string
	State       string
	FrideStatus string
	Archive     string
	Rationale   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ErrNoFrideThread is a thread number fride_threads does not have.
var ErrNoFrideThread = errors.New("no such FrIDE thread")

const frideColumns = `id, chat_jid, brain, message, repos, want_node, node, fride_thread, task_id, branch, state, fride_status, archive, rationale, created_at, updated_at`

func scanFride(row interface{ Scan(...any) error }) (FrideThread, error) {
	var t FrideThread
	var repos string
	err := row.Scan(&t.ID, &t.Chat, &t.Brain, &t.Message, &repos, &t.WantNode, &t.Node, &t.FrideThread, &t.TaskID, &t.Branch, &t.State, &t.FrideStatus, &t.Archive, &t.Rationale, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNoFrideThread
	}
	if err == nil {
		_ = json.Unmarshal([]byte(repos), &t.Repos)
	}
	return t, err
}

// AddFrideThread records a new thread, queued, and returns its number.
func (s *Store) AddFrideThread(ctx context.Context, t FrideThread) (int64, error) {
	repos, err := json.Marshal(nonNil(t.Repos))
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `INSERT INTO fride_threads (chat_jid, brain, message, repos, want_node, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Chat, t.Brain, t.Message, string(repos), t.WantNode, FrideQueued, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SaveFrideThread writes where t is now: node, FrIDE's thread, task,
// branch, state, status and archive.
func (s *Store) SaveFrideThread(ctx context.Context, t FrideThread) error {
	res, err := s.db.ExecContext(ctx, `UPDATE fride_threads SET node = ?, fride_thread = ?, task_id = ?, branch = ?, state = ?, fride_status = ?, archive = ?, rationale = ?, updated_at = ? WHERE id = ?`,
		t.Node, t.FrideThread, t.TaskID, t.Branch, t.State, t.FrideStatus, t.Archive, t.Rationale, time.Now().UTC(), t.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoFrideThread
	}
	return nil
}

// GetFrideThread reads thread id.
func (s *Store) GetFrideThread(ctx context.Context, id int64) (FrideThread, error) {
	return scanFride(s.db.QueryRowContext(ctx, `SELECT `+frideColumns+` FROM fride_threads WHERE id = ?`, id))
}

// FrideThreadOn finds the thread whose work is FrIDE's thread on node.
func (s *Store) FrideThreadOn(ctx context.Context, node string, frideThread int) (FrideThread, error) {
	return scanFride(s.db.QueryRowContext(ctx, `SELECT `+frideColumns+` FROM fride_threads WHERE node = ? AND fride_thread = ? ORDER BY id DESC LIMIT 1`, node, frideThread))
}

// FrideThreads lists the threads in state ("" for all), oldest first.
func (s *Store) FrideThreads(ctx context.Context, state string) ([]FrideThread, error) {
	q, args := `SELECT `+frideColumns+` FROM fride_threads ORDER BY id`, []any{}
	if state != "" {
		q, args = `SELECT `+frideColumns+` FROM fride_threads WHERE state = ? ORDER BY id`, []any{state}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FrideThread
	for rows.Next() {
		t, err := scanFride(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
