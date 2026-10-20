package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestVacuumIntoAndIntegrityCheck(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "live.db")
	db, err := sql.Open("sqlite", "file:"+src+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('kept')"); err != nil {
		t.Fatal(err)
	}
	// The source stays open and in WAL mode, as the bridge's is.
	dst := filepath.Join(dir, "copy.db")
	if err := VacuumInto(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dst)
	// Windows has no Unix mode bits; the data dir's ACL protects the copy.
	if err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Fatalf("copy: %v, %v", st, err)
	}
	if err := IntegrityCheck(context.Background(), dst); err != nil {
		t.Fatal(err)
	}
	cp, err := sql.Open("sqlite", "file:"+dst+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	var v string
	if err := cp.QueryRow("SELECT v FROM t").Scan(&v); err != nil || v != "kept" {
		t.Errorf("copy holds %q, %v", v, err)
	}
	if err := VacuumInto(context.Background(), src, dst); err == nil {
		t.Error("wrote over an existing file")
	}
	if err := VacuumInto(context.Background(), filepath.Join(dir, "none.db"), filepath.Join(dir, "x.db")); err == nil {
		t.Error("snapshot of a missing database")
	}
}

func TestIntegrityCheckRefusesGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.db")
	if err := os.WriteFile(path, []byte("SQLite format 3\x00 but then nothing that makes sense, repeated "+string(make([]byte, 4096))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := IntegrityCheck(context.Background(), path); err == nil {
		t.Error("garbage passed the integrity check")
	}
	if err := IntegrityCheck(context.Background(), filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Error("a missing database passed")
	}
}
