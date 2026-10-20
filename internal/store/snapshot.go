package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
)

// snapshotDSN opens a database for a snapshot or a check, on a connection
// of its own next to whatever else has it open (the running bridge). It
// waits for a lock as long as the bridge's own connections do.
func snapshotDSN(path string) string {
	return DSN(path)
}

// VacuumInto writes a consistent copy of the SQLite database at src to dst
// with VACUUM INTO, which reads one transaction's view of the database
// while others go on writing it: never copy a live database's file, which
// can catch it halfway through a write and misses what is still in its
// WAL. dst must not exist; it is made with mode 0600, since a database the
// bridge keeps (bridge.db above all) holds keys.
func VacuumInto(ctx context.Context, src, dst string) (err error) {
	if _, err := os.Stat(src); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%s exists already", dst)
	}
	// SQLite makes the file with the mode of the umask; make it first, empty,
	// so it keeps 0600. VACUUM INTO accepts an empty file as its target.
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Read-only: VACUUM INTO only reads its source, and a snapshot of a
	// data dir that must stay as it is (a native install's, exported while
	// it moves into the container) must not write to it either.
	db, err := sql.Open("sqlite", snapshotDSN(src)+"&mode=ro")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		return fmt.Errorf("snapshot of %s: %w", src, err)
	}
	return nil
}

// IntegrityCheck runs PRAGMA integrity_check on the database at path and
// returns an error naming what SQLite found unless it answers "ok".
func IntegrityCheck(ctx context.Context, path string) (err error) {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", snapshotDSN(path)+"&mode=ro")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("checking %s: %w", path, err)
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return err
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("checking %s: %w", path, err)
	}
	if len(problems) > 0 {
		if len(problems) > 5 {
			problems = append(problems[:5], fmt.Sprintf("and %d more", len(problems)-5))
		}
		return fmt.Errorf("%s fails SQLite's integrity check: %s", path, strings.Join(problems, "; "))
	}
	return nil
}
