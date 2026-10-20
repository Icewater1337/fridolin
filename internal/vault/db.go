package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite" // the "sqlite" driver
)

// DSN is the data source name of vault.db at path, with the same per-
// connection settings bridge.db gets (store.DSN): foreign keys on, five
// seconds of waiting for a lock, and SQLite's own time format.
func DSN(path string) string {
	return "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_time_format=sqlite"
}

// migrations are vault.db's schema steps, in order; vault_version holds
// how many ran. A step is never edited once released: a change is a new
// step at the end.
var migrations = []string{
	// 1: the index, one row per file below the hot root, and the eviction log.
	`CREATE TABLE files (
		path          TEXT PRIMARY KEY,
		size          INTEGER NOT NULL,
		mtime_ns      INTEGER NOT NULL,
		sha256        TEXT    NOT NULL DEFAULT '',
		tier          TEXT    NOT NULL CHECK (tier IN ('hot', 'cold', 'both')),
		last_access   INTEGER NOT NULL,
		hydrated_at   INTEGER NOT NULL DEFAULT 0,
		pinned        INTEGER NOT NULL DEFAULT 0,
		class         TEXT    NOT NULL DEFAULT 'active' CHECK (class IN ('pinned', 'active', 'cold-eligible')),
		class_reason  TEXT    NOT NULL DEFAULT '',
		cold_key      TEXT    NOT NULL DEFAULT '',
		cold_sha256   TEXT    NOT NULL DEFAULT '',
		uploaded_at   INTEGER NOT NULL DEFAULT 0,
		provenance    TEXT    NOT NULL DEFAULT 'system' CHECK (provenance IN ('owner', 'agent', 'untrusted', 'system')),
		has_companion INTEGER NOT NULL DEFAULT 0,
		updated_at    INTEGER NOT NULL
	);
	CREATE INDEX files_tier_access ON files (tier, last_access);
	CREATE TABLE evictions (
		id     INTEGER PRIMARY KEY AUTOINCREMENT,
		path   TEXT    NOT NULL,
		size   INTEGER NOT NULL,
		reason TEXT    NOT NULL,
		at     INTEGER NOT NULL
	);`,
}

// openDB opens (creating, 0600) and migrates vault.db.
func openDB(path string) (*sql.DB, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("vault: creating %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", DSN(path))
	if err != nil {
		return nil, fmt.Errorf("vault: opening %s: %w", path, err)
	}
	// One connection: the vault serialises its own writers, and SQLite
	// then never sees two of them at once.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		return nil, errors.Join(fmt.Errorf("vault: migrating %s: %w", path, err), db.Close())
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS vault_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var version int
	err := db.QueryRow(`SELECT version FROM vault_version`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := db.Exec(`INSERT INTO vault_version (version) VALUES (0)`); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("vault.db has schema version %d, newer than this build's %d", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			return errors.Join(fmt.Errorf("step %d: %w", i+1, err), tx.Rollback())
		}
		if _, err := tx.Exec(`UPDATE vault_version SET version = ?`, i+1); err != nil {
			return errors.Join(err, tx.Rollback())
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// row is one files row.
type row struct {
	Path         string
	Size         int64
	MtimeNS      int64
	SHA256       string
	Tier         Tier
	LastAccess   int64 // unix ms
	HydratedAt   int64 // unix ms, 0 = never
	Pinned       bool
	Class        Class
	ClassReason  string
	ColdKey      string
	ColdSHA256   string
	UploadedAt   int64 // unix ms
	Provenance   Provenance
	HasCompanion bool
	UpdatedAt    int64 // unix ms
}

const rowColumns = `path, size, mtime_ns, sha256, tier, last_access, hydrated_at, pinned, class, class_reason,
	cold_key, cold_sha256, uploaded_at, provenance, has_companion, updated_at`

type scanner interface{ Scan(...any) error }

func scanRow(s scanner) (row, error) {
	var r row
	err := s.Scan(&r.Path, &r.Size, &r.MtimeNS, &r.SHA256, &r.Tier, &r.LastAccess, &r.HydratedAt, &r.Pinned,
		&r.Class, &r.ClassReason, &r.ColdKey, &r.ColdSHA256, &r.UploadedAt, &r.Provenance, &r.HasCompanion, &r.UpdatedAt)
	return r, err
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// putRow inserts or replaces r.
func putRow(ctx context.Context, db execer, r row) error {
	_, err := db.ExecContext(ctx, `INSERT OR REPLACE INTO files (`+rowColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Path, r.Size, r.MtimeNS, r.SHA256, r.Tier, r.LastAccess, r.HydratedAt, r.Pinned,
		r.Class, r.ClassReason, r.ColdKey, r.ColdSHA256, r.UploadedAt, r.Provenance, r.HasCompanion, r.UpdatedAt)
	return err
}

// getRow returns the row for path, or ok false.
func getRow(ctx context.Context, db *sql.DB, path string) (row, bool, error) {
	r, err := scanRow(db.QueryRowContext(ctx, `SELECT `+rowColumns+` FROM files WHERE path = ?`, path))
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, false, nil
	}
	return r, err == nil, err
}

// queryRows returns the rows a query selects (it must select rowColumns).
func queryRows(ctx context.Context, db *sql.DB, where string, args ...any) ([]row, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+rowColumns+` FROM files `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func deleteRow(ctx context.Context, db execer, path string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM files WHERE path = ?`, path)
	return err
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}
