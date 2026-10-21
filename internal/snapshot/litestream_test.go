package snapshot

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/store"
	_ "modernc.org/sqlite"
)

func TestConfigYAML(t *testing.T) {
	t.Parallel()
	got, err := ConfigYAML([]LitestreamDB{
		{Path: "/x/bridge.db", Replica: Replica{
			Type: "s3", Bucket: "fridolin-snapshots", Path: "litestream/bridge.db",
			Endpoint: "https://s3.eu-central-003.backblazeb2.com", Region: "eu-central-003", ForcePathStyle: true,
		}},
		{Path: "/x/../y/core.db", Replica: Replica{Type: "file", Dir: "/backup/core"}},
		{Path: "/x/aws.db", Replica: Replica{Type: "s3", Bucket: "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := litestreamEconomy + `dbs:
  - path: "/x/bridge.db"
    replica:
      type: s3
      sync-interval: 1m
      bucket: "fridolin-snapshots"
      path: "litestream/bridge.db"
      endpoint: "https://s3.eu-central-003.backblazeb2.com"
      region: "eu-central-003"
      force-path-style: true
  - path: "/y/core.db"
    replica:
      type: file
      sync-interval: 1m
      path: "/backup/core"
  - path: "/x/aws.db"
    replica:
      type: s3
      sync-interval: 1m
      bucket: "b"
`
	if string(got) != want {
		t.Errorf("ConfigYAML =\n%s\nwant\n%s", got, want)
	}

	for _, bad := range [][]LitestreamDB{
		nil,
		{{Path: "rel.db", Replica: Replica{Type: "file", Dir: "/r"}}},
		{{Path: "/a.db", Replica: Replica{Type: "file", Dir: "rel"}}},
		{{Path: "/a.db", Replica: Replica{Type: "s3"}}},
		{{Path: "/a.db", Replica: Replica{Type: "gcs", Bucket: "b"}}},
		{{Path: "/a.db", Replica: Replica{Type: "file", Dir: "/r"}}, {Path: "/a.db", Replica: Replica{Type: "file", Dir: "/s"}}},
	} {
		if _, err := ConfigYAML(bad); err == nil {
			t.Errorf("ConfigYAML(%+v) accepted it", bad)
		}
	}
}

func TestYAMLString(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"plain", "no", "1e3", `a"b\c`, "key: value # x", "$HOME/${X}", "tab\tnew\nline", "<&>", "ünïcode"} {
		q := yamlString(s)
		if !strings.HasPrefix(q, `"`) || strings.ContainsAny(q, "\n\t") {
			t.Errorf("yamlString(%q) = %s", s, q)
		}
		var back string
		if err := json.Unmarshal([]byte(q), &back); err != nil || back != s {
			t.Errorf("yamlString(%q) = %s reads back as %q, %v", s, q, back, err)
		}
	}
}

func TestLineWriter(t *testing.T) {
	t.Parallel()
	var lines []string
	w := &lineWriter{secrets: []string{"s3cret"}, emit: func(s string) { lines = append(lines, s) }}
	_, _ = io.WriteString(w, "level=INFO msg=one\nlevel=ERROR msg=\"bad s3cret\"\r\npart")
	_, _ = io.WriteString(w, "ial\n\nlast")
	w.flush()
	want := []string{"level=INFO msg=one", `level=ERROR msg="bad ***"`, "partial", "last"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("lines = %q, want %q", lines, want)
	}
	if got := w.lastError(); got != `level=ERROR msg="bad ***"` {
		t.Errorf("lastError = %q", got)
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestLitestreamMissingBinary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l := &Litestream{
		Binary:     filepath.Join(dir, "no-such-litestream"),
		DBs:        []LitestreamDB{{Path: filepath.Join(dir, "x.db"), Replica: Replica{Type: "file", Dir: filepath.Join(dir, "r")}}},
		TempDir:    dir,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		MinBackoff: 5 * time.Millisecond,
		MaxBackoff: 20 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	waitFor(t, 5*time.Second, "restarts", func() bool { return l.Status().Restarts >= 3 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	st := l.Status()
	if st.Running || st.LastError == "" || st.LastExit.IsZero() {
		t.Errorf("status = %+v", st)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "litestream-*.yml")); len(left) != 0 {
		t.Errorf("Run left its configuration behind: %v", left)
	}

	bad := &Litestream{Binary: l.Binary, DBs: []LitestreamDB{{Path: "rel.db"}}}
	if err := bad.Run(context.Background()); err == nil {
		t.Error("Run with a bad configuration returned nil")
	}
}

func waitFor(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", limit, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func realLitestream(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("runs litestream")
	}
	bin, err := exec.LookPath("litestream")
	if err != nil {
		if _, serr := os.Stat("/opt/homebrew/bin/litestream"); serr != nil {
			t.Skip("litestream is not installed")
		}
		bin = "/opt/homebrew/bin/litestream"
	}
	return bin
}

func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", store.DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func rowCount(t *testing.T, path string) int {
	t.Helper()
	db := openDB(t, path)
	var check string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity_check of %s = %q, %v", path, check, err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	return n
}

func TestLitestreamReal(t *testing.T) {
	t.Parallel()
	bin := realLitestream(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data", "app.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	replica := filepath.Join(dir, "replica")
	tmp := filepath.Join(dir, "tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	db := openDB(t, dbPath)
	for _, q := range []string{"PRAGMA journal_mode=WAL", "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(from, to int) {
		t.Helper()
		for i := from; i < to; i++ {
			if _, err := db.Exec("INSERT INTO t (v) VALUES (?)", fmt.Sprintf("row %d", i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	insert(0, 50)

	out := &syncBuffer{}
	l := &Litestream{
		Binary:     bin,
		DBs:        []LitestreamDB{{Path: dbPath, Replica: Replica{Type: "file", Dir: replica}}},
		Env:        []string{"HOME=" + dir, "PATH=/usr/bin:/bin", "LITESTREAM_SECRET_ACCESS_KEY=never-shown"},
		TempDir:    tmp,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		MinBackoff: 50 * time.Millisecond,
		Stderr:     out,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()

	waitFor(t, 10*time.Second, "litestream to start", func() bool { return l.Status().Running })
	first := l.Status()
	if first.PID == 0 || first.Since.IsZero() || first.Restarts != 0 {
		t.Errorf("status after start = %+v", first)
	}
	cfgs, _ := filepath.Glob(filepath.Join(tmp, "litestream-*.yml"))
	if len(cfgs) != 1 {
		t.Fatalf("configuration files = %v, want one", cfgs)
	}
	if fi, err := os.Stat(cfgs[0]); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("configuration file mode = %v, want 0600", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(cfgs[0]); strings.Contains(string(b), "never-shown") {
		t.Error("the configuration file holds a credential")
	}

	// Each restore is a fresh file: litestream will not overwrite one.
	restores := 0
	restoredRows := func() int {
		restores++
		target := filepath.Join(dir, fmt.Sprintf("restore-%d.db", restores))
		if err := l.Restore(ctx, dbPath, target); err != nil {
			return -1 // nothing replicated yet
		}
		return rowCount(t, target)
	}
	waitFor(t, 30*time.Second, "the first rows in the replica", func() bool { return restoredRows() == 50 })

	// Killed from outside, litestream is started again.
	proc, err := os.FindProcess(first.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "a restart", func() bool {
		st := l.Status()
		return st.Running && st.Restarts == 1 && st.PID != first.PID
	})
	if st := l.Status(); st.LastError == "" || st.LastExit.IsZero() {
		t.Errorf("status after the kill = %+v, want the exit recorded", st)
	}

	insert(50, 80)
	waitFor(t, 30*time.Second, "the later rows in the replica", func() bool { return restoredRows() == 80 })

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if st := l.Status(); st.Running || st.PID != 0 {
		t.Errorf("status after Run = %+v", st)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "litestream-*.yml")); len(left) != 0 {
		t.Errorf("Run left its configuration behind: %v", left)
	}
	if strings.Contains(out.String(), "never-shown") {
		t.Error("litestream's output shows a credential")
	}
	if !strings.Contains(out.String(), "replicating to") {
		t.Errorf("litestream's output did not reach Stderr: %q", out.String())
	}

	// After Run, Restore still reads the replica.
	final := filepath.Join(dir, "final.db")
	if err := l.Restore(context.Background(), dbPath, final); err != nil {
		t.Fatal(err)
	}
	if n := rowCount(t, final); n != 80 {
		t.Errorf("restored %d rows, want 80", n)
	}
	if err := l.Restore(context.Background(), dbPath, final); err == nil {
		t.Error("Restore over an existing file ran")
	}
	if err := l.Restore(context.Background(), filepath.Join(dir, "other.db"), filepath.Join(dir, "o.db")); err == nil {
		t.Error("Restore of a database that is not replicated ran")
	}
}
