package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/logx"
	"log"
	"os"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/store/sqlstore"
	_ "modernc.org/sqlite"
)

// Store manages both the whatsmeow device store and the bridge's own tables.
type Store struct {
	db        *sql.DB
	Container *sqlstore.Container
	closeOnce sync.Once
	closeErr  error
}

// Open opens (or creates) the SQLite database and initialises both the
// whatsmeow device container and the bridge's own tables. When it fails it
// closes whatever it had opened, since the caller gets no store to close:
// NewBridge gives the instance lock back on that error, and whoever takes it
// next must be the only one with bridge.db open.
func Open(dbPath, logLevel string) (*Store, error) {
	if err := makePrivateDB(dbPath); err != nil {
		return nil, fmt.Errorf("creating database file: %w", err)
	}
	dbURL := DSN(dbPath)

	logger := logx.NewWaLogger("Store", logLevel)

	db, err := sql.Open("sqlite", dbURL)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite db: %w", err)
	}
	// The device container and application tables share one pool and owner.
	// "sqlite3" names whatsmeow's SQL dialect, not the driver.
	container := sqlstore.NewWithDB(db, "sqlite3", logger)
	if err := container.Upgrade(context.Background()); err != nil {
		return nil, errors.Join(fmt.Errorf("creating sqlstore container: failed to upgrade database: %w", err), container.Close())
	}

	s := &Store{db: db, Container: container}
	if err := s.migrate(); err != nil {
		return nil, errors.Join(fmt.Errorf("migrating store: %w", err), s.Close())
	}
	return s, nil
}

// DSN is the data source name, for the "sqlite" driver, of the database at
// path, with the settings every connection to bridge.db needs. The pure-Go
// driver applies them per connection, as the cgo driver this replaced did with
// its own parameters; busy_timeout keeps the five seconds that driver waited
// for a lock by default, and _time_format=sqlite writes times in the format
// that driver wrote, so rows from before the switch compare and sort as new
// ones do.
func DSN(path string) string {
	return "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_time_format=sqlite"
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
		if err := fsx.RestrictToOwner(p); err != nil {
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

// appMigrations are the bridge's own schema changes, in order. The
// version fridolin_version holds is how many of them a database has had.
// whatsmeow keeps its own in whatsmeow_version, and PRAGMA user_version
// stays unused, so that neither versioning can step on the other. Each
// change only adds, so that an older bridge rolled back to still finds
// the tables and columns it reads.
var appMigrations = []func(*sql.Tx) error{
	migrateV1,
	migrateV2,
	migrateV3,
	migrateV4,
	migrateV5,
	migrateV6,
}

// migrate brings the bridge's tables to the newest version, one
// migration per transaction.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS fridolin_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var version int
	err := s.db.QueryRow(`SELECT version FROM fridolin_version`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.db.Exec(`INSERT INTO fridolin_version (version) VALUES (0)`); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	for v := version; v < len(appMigrations); v++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if err := appMigrations[v](tx); err != nil {
			return errors.Join(fmt.Errorf("migration %d: %w", v+1, err), tx.Rollback())
		}
		if _, err := tx.Exec(`UPDATE fridolin_version SET version = ?`, v+1); err != nil {
			return errors.Join(err, tx.Rollback())
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// migrateV1 makes the tables from before the migrations were versioned. A
// database from then has them already, so each is made only if missing.
func migrateV1(tx *sql.Tx) error {
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
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// migrateV2 records which brain sent a poll, so that a vote goes to that
// brain's history: one brain may send to a chat that another owns. Polls
// from before have none and go to the brain that owns their chat.
func migrateV2(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE polls ADD COLUMN brain TEXT NOT NULL DEFAULT ''`)
	return err
}

// SavePoll stores a poll brain sent, so votes can be matched back to
// option names and reach that brain.
func (s *Store) SavePoll(msgID, chatJID, brain, question string, options []string) error {
	return s.SavePollRef(msgID, chatJID, brain, question, options, "")
}

// SavePollRef is SavePoll for a poll the bridge sent for ref, an
// approval say ("approval:<id>"), whose votes go there.
func (s *Store) SavePollRef(msgID, chatJID, brain, question string, options []string, ref string) error {
	optJSON, err := json.Marshal(options)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO polls (msg_id, chat_jid, brain, question, options, created_at, ref) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msgID, chatJID, brain, question, string(optJSON), time.Now().UTC(), ref,
	)
	return err
}

// SentPoll is what the bridge keeps of a poll it sent, for its votes.
type SentPoll struct {
	Chat     string
	Brain    string // "" for a poll from before polls recorded it
	Question string
	Options  []string
	// Ref names what the bridge sent the poll for (SavePollRef).
	Ref string
}

// SentPoll returns the poll sent as message msgID, or sql.ErrNoRows.
func (s *Store) SentPoll(ctx context.Context, msgID string) (SentPoll, error) {
	var p SentPoll
	var optJSON string
	err := s.db.QueryRowContext(ctx, `SELECT chat_jid, brain, question, options, ref FROM polls WHERE msg_id = ?`, msgID).
		Scan(&p.Chat, &p.Brain, &p.Question, &optJSON, &p.Ref)
	if err != nil {
		return SentPoll{}, err
	}
	p.Options = jsonStrings(optJSON)
	return p, nil
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

// PollResult is a poll the bridge sent, with its votes as they stand.
type PollResult struct {
	MsgID    string   `json:"msg_id"`
	ChatJID  string   `json:"chat_jid"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	// TotalVoters counts the people with a current vote; someone who took
	// theirs back is not one.
	TotalVoters int          `json:"total_voters"`
	Results     []PollOption `json:"results"`
	CreatedAt   time.Time    `json:"created_at"`
}

// PollOption is one option of a poll and who chose it.
type PollOption struct {
	Option string   `json:"option"`
	Votes  int      `json:"votes"`
	Voters []string `json:"voters"`
}

// Poll returns the poll sent as message msgID, or sql.ErrNoRows.
func (s *Store) Poll(ctx context.Context, msgID string) (*PollResult, error) {
	var p PollResult
	var options string
	err := s.db.QueryRowContext(ctx, `SELECT msg_id, chat_jid, question, options, created_at FROM polls WHERE msg_id = ?`, msgID).
		Scan(&p.MsgID, &p.ChatJID, &p.Question, &options, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	p.Options = jsonStrings(options)
	if err := s.tally(ctx, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// RecentPolls returns up to limit polls, newest first, of the chats keep
// accepts, only those in chat when it is not "".
func (s *Store) RecentPolls(ctx context.Context, chat string, limit int, keep func(chat string) bool) ([]PollResult, error) {
	query, args := `SELECT msg_id, chat_jid, question, options, created_at FROM polls`, []any{}
	if chat != "" {
		query, args = query+` WHERE chat_jid = ?`, append(args, chat)
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	polls := []PollResult{}
	for len(polls) < limit && rows.Next() {
		var p PollResult
		var options string
		if err := rows.Scan(&p.MsgID, &p.ChatJID, &p.Question, &options, &p.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if keep(p.ChatJID) {
			p.Options = jsonStrings(options)
			polls = append(polls, p)
		}
	}
	// Closed before the votes are read: each poll's are another query.
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for i := range polls {
		if err := s.tally(ctx, &polls[i]); err != nil {
			return nil, err
		}
	}
	return polls, nil
}

// tally counts p's votes into its results.
func (s *Store) tally(ctx context.Context, p *PollResult) error {
	p.Results = make([]PollOption, len(p.Options))
	index := map[string]int{}
	for i, opt := range p.Options {
		p.Results[i] = PollOption{Option: opt, Voters: []string{}}
		index[opt] = i
	}
	rows, err := s.db.QueryContext(ctx, `SELECT voter_jid, selected_options FROM poll_votes WHERE poll_msg_id = ? ORDER BY voted_at, voter_jid`, p.MsgID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var voter, selected string
		if err := rows.Scan(&voter, &selected); err != nil {
			return err
		}
		// A vote taken back (every option deselected) is stored as null.
		chosen := jsonStrings(selected)
		if len(chosen) > 0 {
			p.TotalVoters++
		}
		for _, opt := range chosen {
			if i, ok := index[opt]; ok {
				p.Results[i].Votes++
				p.Results[i].Voters = append(p.Results[i].Voters, voter)
			}
		}
	}
	return rows.Err()
}

// jsonStrings decodes a JSON array of strings column, treating anything
// else as empty.
func jsonStrings(s string) []string {
	var out []string
	if json.Unmarshal([]byte(s), &out) != nil || out == nil {
		return []string{}
	}
	return out
}

// Close closes the shared pool once, after all database consumers have stopped.
func (s *Store) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.Container.Close() })
	return s.closeErr
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
