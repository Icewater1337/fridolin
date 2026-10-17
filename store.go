package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow/store/sqlstore"
)

// Store manages both the whatsmeow device store and the bridge's own tables.
type Store struct {
	db        *sql.DB
	Container *sqlstore.Container
}

// NewStore opens (or creates) the SQLite database and initialises both the
// whatsmeow device container and the bridge's own tables. When it fails it
// closes whatever it had opened, since the caller gets no store to close:
// NewBridge gives the instance lock back on that error, and whoever takes it
// next must be the only one with bridge.db open.
func NewStore(cfg *Config) (*Store, error) {
	if err := makePrivateDB(cfg.DBPath); err != nil {
		return nil, fmt.Errorf("creating database file: %w", err)
	}
	dbURL := "file:" + cfg.DBPath + "?_foreign_keys=on"

	logger := newWaLogger("Store", cfg.LogLevel)

	container, err := sqlstore.New(context.Background(), "sqlite3", dbURL, logger)
	if err != nil {
		return nil, fmt.Errorf("creating sqlstore container: %w", err)
	}

	// Open a plain sql.DB on the same file for the bridge's own tables.
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

// sqliteSidecars are the suffixes of the files SQLite keeps next to a
// database: the rollback journal, the write-ahead log and its index.
var sqliteSidecars = []string{"-journal", "-wal", "-shm"}

// makePrivateDB creates the database file with mode 0600 before SQLite opens
// it, and takes group and other access away from an existing one. The file
// holds the WhatsApp device's identity keys: whoever can read it can
// impersonate the linked device. Left to itself, SQLite creates it with 0644
// minus the umask. SQLite gives the journal and WAL files the database
// file's mode, so new ones follow; leftovers from before are fixed here too.
func makePrivateDB(path string) error {
	// Read-only is enough to create the file, and an existing database the
	// bridge may not write gets SQLite's own error instead of one from here.
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	for _, p := range append([]string{path}, sidecarPaths(path)...) {
		// Not fatal: a database the bridge can open but not chmod (another
		// user's file) worked before, and refusing to start would take the
		// bridge offline.
		if err := restrictToOwner(p); err != nil {
			log.Printf("warning: could not make %s private: %v", p, err)
		}
	}
	return nil
}

func sidecarPaths(dbPath string) []string {
	out := make([]string, len(sqliteSidecars))
	for i, suffix := range sqliteSidecars {
		out[i] = dbPath + suffix
	}
	return out
}

// restrictToOwner takes group and other permissions away from an existing
// file or directory, and logs when it had to. It never adds permissions,
// keeps the setuid, setgid and sticky bits, and leaves a path that does not
// exist alone.
func restrictToOwner(path string) error {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	old := st.Mode()
	if old.Perm()&0o077 == 0 {
		return nil
	}
	// Chmod sets the special bits too, so pass them on or they are lost:
	// a setgid folder would stop handing its group to new files.
	mode := old & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky) &^ 0o077
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	log.Printf("%s was open to other users (mode %04o); changed it to %04o", path, uint32(old.Perm()), uint32(mode.Perm()))
	return nil
}

func (s *Store) migrate() error {
	stmts := []string{
		// sessions and process_state are written no more: classic mode kept
		// each chat's Claude session in one, and runs kept their PID in the
		// other, which nothing read. Both stay, so that an older bridge
		// rolled back to finds the tables it expects.
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
