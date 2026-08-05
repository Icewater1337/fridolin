package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Store manages both the whatsmeow device store and the sessions table.
type Store struct {
	db        *sql.DB
	Container *sqlstore.Container
}

// NewStore opens (or creates) the SQLite database and initialises both
// the whatsmeow device container and the sessions table.
func NewStore(cfg *Config) (*Store, error) {
	dbURL := "file:" + cfg.DBPath + "?_foreign_keys=on"

	logger := waLog.Stdout("Store", cfg.LogLevel, true)

	container, err := sqlstore.New(context.Background(), "sqlite3", dbURL, logger)
	if err != nil {
		return nil, fmt.Errorf("creating sqlstore container: %w", err)
	}

	// Open a plain sql.DB on the same file for the sessions table.
	db, err := sql.Open("sqlite3", dbURL)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite db: %w", err)
	}

	s := &Store{db: db, Container: container}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrating store: %w", err)
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

// Close closes the plain DB connection (the whatsmeow container manages its own).
func (s *Store) Close() error {
	return s.db.Close()
}
