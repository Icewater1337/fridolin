package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow/store/sqlstore"
)

// Store manages both the whatsmeow device store and the sessions table.
type Store struct {
	db        *sql.DB
	Container *sqlstore.Container
}

// NewStore opens (or creates) the SQLite database and initialises both
// the whatsmeow device container and the sessions table. When it fails it
// closes whatever it had opened, since the caller gets no store to close:
// NewBridge gives the instance lock back on that error, and whoever takes it
// next must be the only one with bridge.db open.
func NewStore(cfg *Config) (*Store, error) {
	dbURL := "file:" + cfg.DBPath + "?_foreign_keys=on"

	logger := newWaLogger("Store", cfg.LogLevel)

	container, err := sqlstore.New(context.Background(), "sqlite3", dbURL, logger)
	if err != nil {
		return nil, fmt.Errorf("creating sqlstore container: %w", err)
	}

	// Open a plain sql.DB on the same file for the sessions table.
	db, err := sql.Open("sqlite3", dbURL)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("opening sqlite db: %w", err), container.Close())
	}

	s := &Store{db: db, Container: container}
	if err := s.migrate(); err != nil {
		return nil, errors.Join(fmt.Errorf("migrating store: %w", err), s.Close())
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS sessions (
			jid        TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS process_state (
			jid        TEXT PRIMARY KEY,
			pid        INTEGER NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		// polls — one row per poll we send; stores question and option names so
		// we can match vote hashes back to human-readable option names.
		`CREATE TABLE IF NOT EXISTS polls (
			msg_id     TEXT PRIMARY KEY,
			chat_jid   TEXT NOT NULL,
			question   TEXT NOT NULL,
			options    TEXT NOT NULL, -- JSON array of option name strings
			created_at DATETIME NOT NULL
		)`,
		// poll_votes — one row per (poll, voter); INSERT OR REPLACE so re-votes update in place.
		`CREATE TABLE IF NOT EXISTS poll_votes (
			poll_msg_id      TEXT NOT NULL,
			voter_jid        TEXT NOT NULL,
			selected_options TEXT NOT NULL, -- JSON array of chosen option name strings
			voted_at         DATETIME NOT NULL,
			PRIMARY KEY (poll_msg_id, voter_jid)
		)`,
		// ignored_chats — chats that wrote but are not allowed, so the
		// dashboard can offer to allow them. No message content is stored.
		`CREATE TABLE IF NOT EXISTS ignored_chats (
			jid        TEXT PRIMARY KEY,
			kind       TEXT NOT NULL,
			name       TEXT NOT NULL DEFAULT '',
			sender     TEXT NOT NULL DEFAULT '',
			push_name  TEXT NOT NULL DEFAULT '',
			count      INTEGER NOT NULL DEFAULT 0,
			first_seen DATETIME NOT NULL,
			last_seen  DATETIME NOT NULL,
			dismissed  INTEGER NOT NULL DEFAULT 0
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// SavePoll stores poll metadata so votes can be matched back to option names.
func (s *Store) SavePoll(msgID, chatJID, question string, options []string) error {
	optJSON, err := json.Marshal(options)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO polls (msg_id, chat_jid, question, options, created_at) VALUES (?, ?, ?, ?, ?)`,
		msgID, chatJID, question, string(optJSON), time.Now().UTC(),
	)
	return err
}

// GetPollOptions returns the question and option names for a poll by message ID.
func (s *Store) GetPollOptions(msgID string) (question string, options []string, err error) {
	var optJSON string
	err = s.db.QueryRow(`SELECT question, options FROM polls WHERE msg_id = ?`, msgID).Scan(&question, &optJSON)
	if err != nil {
		return
	}
	err = json.Unmarshal([]byte(optJSON), &options)
	return
}

// SavePollVote upserts a voter's selections for a poll (re-votes replace old ones).
func (s *Store) SavePollVote(pollMsgID, voterJID string, selectedOptions []string) error {
	selJSON, err := json.Marshal(selectedOptions)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO poll_votes (poll_msg_id, voter_jid, selected_options, voted_at) VALUES (?, ?, ?, ?)`,
		pollMsgID, voterJID, string(selJSON), time.Now().UTC(),
	)
	return err
}

// GetSession returns the stored Claude session ID for the given JID,
// or an empty string if no session is stored.
func (s *Store) GetSession(jid string) (string, error) {
	var sessionID string
	err := s.db.QueryRow(`SELECT session_id FROM sessions WHERE jid = ?`, jid).Scan(&sessionID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("querying session for %q: %w", jid, err)
	}
	return sessionID, nil
}

// SaveSession upserts the Claude session ID for the given JID.
func (s *Store) SaveSession(jid, sessionID string) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions (jid, session_id, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(jid) DO UPDATE SET session_id = excluded.session_id, updated_at = excluded.updated_at`,
		jid, sessionID, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("saving session for %q: %w", jid, err)
	}
	return nil
}

// DeleteSession removes the session record for the given JID.
func (s *Store) DeleteSession(jid string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE jid = ?`, jid)
	if err != nil {
		return fmt.Errorf("deleting session for %q: %w", jid, err)
	}
	return nil
}

// SaveProcessPID upserts the active process PID for the given JID.
func (s *Store) SaveProcessPID(jid string, pid int) error {
	_, err := s.db.Exec(
		`INSERT INTO process_state (jid, pid, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(jid) DO UPDATE SET pid = excluded.pid, updated_at = excluded.updated_at`,
		jid, pid, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("saving process pid for %q: %w", jid, err)
	}
	return nil
}

// GetProcessPID returns the active process PID for the given JID, or 0 if unset.
func (s *Store) GetProcessPID(jid string) (int, error) {
	var pid int
	err := s.db.QueryRow(`SELECT pid FROM process_state WHERE jid = ?`, jid).Scan(&pid)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("querying process pid for %q: %w", jid, err)
	}
	return pid, nil
}

// DeleteProcessPID removes the active process PID record for the given JID.
func (s *Store) DeleteProcessPID(jid string) error {
	_, err := s.db.Exec(`DELETE FROM process_state WHERE jid = ?`, jid)
	if err != nil {
		return fmt.Errorf("deleting process pid for %q: %w", jid, err)
	}
	return nil
}

// Close closes both connections to the database: the plain one and the
// whatsmeow container's, which nothing else closes.
func (s *Store) Close() error {
	return errors.Join(s.db.Close(), s.Container.Close())
}

// IgnoredChat is a chat that messaged the bridge without being allowed.
type IgnoredChat struct {
	JID       string    `json:"jid"`
	Kind      string    `json:"kind"` // dm or group
	Name      string    `json:"name"`
	Sender    string    `json:"sender"`
	PushName  string    `json:"push_name"`
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// RecordIgnored counts a message from a chat that is not allowed. A dismissed
// chat stays dismissed.
func (s *Store) RecordIgnored(kind, jid, name, sender, pushName string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`INSERT INTO ignored_chats (jid, kind, name, sender, push_name, count, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
			count = count + 1, last_seen = excluded.last_seen,
			name = CASE WHEN excluded.name != '' THEN excluded.name ELSE name END,
			sender = excluded.sender,
			push_name = CASE WHEN excluded.push_name != '' THEN excluded.push_name ELSE push_name END`,
		jid, kind, name, sender, pushName, now, now)
	return err
}

// ListIgnored returns chats that wrote recently and were not dismissed.
func (s *Store) ListIgnored() ([]IgnoredChat, error) {
	rows, err := s.db.Query(`SELECT jid, kind, name, sender, push_name, count, first_seen, last_seen
		FROM ignored_chats WHERE dismissed = 0 ORDER BY last_seen DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IgnoredChat{}
	for rows.Next() {
		var c IgnoredChat
		if err := rows.Scan(&c.JID, &c.Kind, &c.Name, &c.Sender, &c.PushName, &c.Count, &c.FirstSeen, &c.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DismissIgnored hides a chat from the dashboard list (or removes it once it
// has been allowed).
func (s *Store) DismissIgnored(jid string, remove bool) error {
	if remove {
		_, err := s.db.Exec(`DELETE FROM ignored_chats WHERE jid = ?`, jid)
		return err
	}
	_, err := s.db.Exec(`UPDATE ignored_chats SET dismissed = 1 WHERE jid = ?`, jid)
	return err
}
