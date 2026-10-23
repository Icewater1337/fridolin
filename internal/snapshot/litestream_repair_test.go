package snapshot

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNeedsRepair(t *testing.T) {
	t.Parallel()
	missing := `time=2026-10-22T16:24:11.020Z level=ERROR msg="monitor error" system=store db=bridge.db replica=file ` +
		`error="open ltx file /data/.bridge.db-litestream/ltx/0/0000000000000001-0000000000000001.ltx: no such file or directory" ` +
		`hint="LTX file is missing. This can happen after VACUUM, manual checkpoint, or state corruption."`
	for _, tc := range []struct {
		line string
		db   string
		ok   bool
	}{
		{missing, "bridge.db", true},
		{strings.Replace(missing, "db=bridge.db", `db="vault.db"`, 1), "vault.db", true},
		{`level=ERROR msg="monitor error" db=bridge.db error="connection refused"`, "", false},
		{`level=ERROR hint="LTX file is missing"`, "", false}, // no database named
		{"", "", false},
	} {
		db, ok := needsRepair(tc.line)
		if db != tc.db || ok != tc.ok {
			t.Errorf("needsRepair(%.60q) = %q, %v; want %q, %v", tc.line, db, ok, tc.db, tc.ok)
		}
	}
}

// fakeLitestream writes a litestream that, for replicate, logs the
// missing-LTX error for app.db until it was reset once, and then logs
// nothing; reset appends its arguments to calls.
func fakeLitestream(t *testing.T, dir string) (bin, calls string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake litestream is a shell script")
	}
	bin, calls = filepath.Join(dir, "litestream"), filepath.Join(dir, "calls")
	script := `#!/bin/sh
case "$1" in
reset) echo "$@" >>"` + calls + `"; echo "Reset complete."; exit 0 ;;
replicate)
  if [ -f "` + calls + `" ]; then echo 'level=INFO msg="replicating to" db=app.db'; exec sleep 60; fi
  while :; do
    echo 'time=x level=ERROR msg="monitor error" system=store db=app.db replica=file error="open ltx file: no such file or directory" hint="LTX file is missing. Run litestream reset"'
    sleep 0.05
  done ;;
esac
exit 2
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, calls
}

func TestLitestreamRepairsMissingLTX(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin, calls := fakeLitestream(t, dir)
	dbPath := filepath.Join(dir, "app.db")
	replica := filepath.Join(dir, "replica", "app.db")
	if err := os.MkdirAll(filepath.Join(replica, "ltx", "9"), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(replica, "ltx", "9", "0000000000000001-0000000000000016.ltx")
	if err := os.WriteFile(stale, []byte("older snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := &Litestream{
		Binary: bin,
		DBs: []LitestreamDB{
			{Path: dbPath, Replica: Replica{Type: "file", Dir: replica}},
			{Path: filepath.Join(dir, "other.db"), Replica: Replica{Type: "file", Dir: filepath.Join(dir, "replica", "other.db")}},
		},
		Env:        []string{"PATH=/usr/bin:/bin"},
		TempDir:    dir,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		MinBackoff: 10 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()

	waitFor(t, 10*time.Second, "a repair", func() bool { return l.Status().Repairs == 1 })
	waitFor(t, 10*time.Second, "litestream to run again", func() bool { return l.Status().Running })
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); !strings.HasPrefix(got, "reset -no-expand-env -config ") || !strings.HasSuffix(got, " "+dbPath) {
		t.Errorf("reset was called as %q", got)
	}
	if _, err := os.Stat(replica); !os.IsNotExist(err) {
		t.Errorf("the replica was not emptied: %v", err)
	}
	st := l.Status()
	if st.LastRepair.IsZero() || !strings.Contains(st.LastProblem, "LTX file is missing") || st.LastProblemAt.IsZero() {
		t.Errorf("status = %+v", st)
	}
	if st.LastError != "" {
		t.Errorf("a repair was recorded as an error: %q", st.LastError)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if n := strings.Count(string(b), "reset"); n != 1 {
		t.Errorf("reset %d times, want once", n)
	}
}

func TestLitestreamRepairSpacedAndOnlyForFileReplicas(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin, calls := fakeLitestream(t, dir)
	l := &Litestream{
		Binary: bin,
		DBs: []LitestreamDB{{Path: filepath.Join(dir, "app.db"),
			Replica: Replica{Type: "s3", Bucket: "b", Path: "litestream/app.db"}}},
		Env:        []string{"PATH=/usr/bin:/bin"},
		TempDir:    dir,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		MinBackoff: 10 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	// A replica in a bucket cannot be emptied from here: no reset, which
	// would leave the older snapshot to be restored; one stop at most an
	// hour, and the problem stays in the status for doctor.
	waitFor(t, 10*time.Second, "the problem in the status", func() bool { return l.Status().LastProblem != "" })
	time.Sleep(500 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Errorf("reset was called for an s3 replica")
	}
	if st := l.Status(); st.Repairs != 0 || st.Restarts > 1 {
		t.Errorf("status = %+v, want no repair and at most one stop", st)
	}
}
