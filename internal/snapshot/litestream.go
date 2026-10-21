package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Replica is where litestream copies one database to.
type Replica struct {
	Type string // "s3" or "file"

	// For "s3": the bucket, the path in it, and for a service other than
	// AWS (Backblaze B2, MinIO) its endpoint and region.
	Bucket, Path, Endpoint, Region string
	ForcePathStyle                 bool

	// For "file": the folder.
	Dir string
}

// LitestreamDB is a SQLite database and its replica.
type LitestreamDB struct {
	Path    string
	Replica Replica
}

// Litestream supervises `litestream replicate`, which follows each
// database's write-ahead log and copies every change to its replica
// within about a second.
type Litestream struct {
	Binary string // path of the litestream executable
	DBs    []LitestreamDB
	// Env is the child's whole environment. S3 credentials go here, as
	// LITESTREAM_ACCESS_KEY_ID and LITESTREAM_SECRET_ACCESS_KEY, never
	// into the configuration file.
	Env []string
	// TempDir is where the generated configuration file is written; ""
	// is the system's temporary folder.
	TempDir string
	Logger  *slog.Logger // nil is slog.Default()
	// MinBackoff and MaxBackoff bound the wait before a child that exited
	// is started again (default 1s and 1m). The wait doubles with each
	// exit and starts over once a child ran for restartReset.
	MinBackoff, MaxBackoff time.Duration
	// Stderr, when set, gets the child's output, a line at a time, with
	// secrets blanked. Otherwise its lines go to Logger: warnings and
	// errors as warnings, the rest at debug level.
	Stderr io.Writer

	mu     sync.Mutex
	status LitestreamStatus
}

// LitestreamStatus is how replication is doing.
type LitestreamStatus struct {
	Running   bool      `json:"running"`
	PID       int       `json:"pid,omitempty"`
	Since     time.Time `json:"since,omitzero"` // when the running child started
	Restarts  int       `json:"restarts"`       // children started after the first
	LastError string    `json:"last_error,omitempty"`
	LastExit  time.Time `json:"last_exit,omitzero"`
}

// restartReset is how long a child must have run for the backoff to start
// over: one that fails at once is retried ever more slowly, one that ran
// for a while and then lost its connection is retried soon.
const restartReset = 5 * time.Minute

// litestreamWaitDelay is how long a child that was told to stop may take
// to upload what it has left before it is killed.
const litestreamWaitDelay = 10 * time.Second

// yamlString quotes s for YAML. A JSON string is a valid YAML
// double-quoted scalar, and quoting every value keeps a path such as
// "no", "1e3" or one with a colon from being read as something else.
func yamlString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

// litestreamEconomy is the global part of the configuration: how often
// Litestream lists and compacts its replica. Its defaults (L0 retention
// checked every 15 seconds, compaction every 30 seconds, 5 minutes and an
// hour) list the replica about 24 times a minute for two databases even
// while nothing is written: measured against a counting S3 proxy, some
// 34,000 LIST calls a day, each a Class C transaction on B2, whose free
// tier caps them at 2,500. Uploads (PUT, Class A) are free, so the sync
// interval does not count against a cap; listings do, and a listing is
// one call per thousand files, so syncing each minute rather than each
// second also keeps the level-0 folder short. With these settings the
// steady state lists nothing between the hourly compaction and the
// six-hourly retention check: about 2 LISTs each per database, some 200
// Class C a day for both, plus about 34 at each start (docs/VAULT.md, "B2
// transactions").
const litestreamEconomy = `l0-retention: 1h
l0-retention-check-interval: 6h
levels:
  - interval: 1h
  - interval: 24h
snapshot:
  interval: 24h
`

// litestreamSyncInterval is how often each replica uploads what changed:
// the most of the databases a disaster loses.
const litestreamSyncInterval = "1m"

// LitestreamDailyCost is what one running Litestream with n databases
// spends on B2 a day in its steady state, by the measurement above: per
// database 24 compactions and 4 retention checks of about 2 listings each,
// a daily compaction and snapshot, and the downloads of the daily
// compaction (Class B).
func LitestreamDailyCost(n int) (classB, classC float64) {
	return 25 * float64(n), 105 * float64(n)
}

// LitestreamStartCost is what one start of Litestream with n databases
// spends: it lists every level of each database and compacts them.
func LitestreamStartCost(n int) (classB, classC float64) {
	return 1 * float64(n), 17 * float64(n)
}

// ConfigYAML is the configuration litestream 0.5 reads for dbs. It holds
// no credentials: litestream reads those from its environment.
func ConfigYAML(dbs []LitestreamDB) ([]byte, error) {
	if len(dbs) == 0 {
		return nil, errors.New("snapshot: no databases to replicate")
	}
	var b strings.Builder
	b.WriteString(litestreamEconomy)
	b.WriteString("dbs:\n")
	seen := map[string]bool{}
	for _, db := range dbs {
		if !filepath.IsAbs(db.Path) {
			return nil, fmt.Errorf("snapshot: database path %q is not absolute", db.Path)
		}
		clean := filepath.Clean(db.Path)
		if seen[clean] {
			return nil, fmt.Errorf("snapshot: database %s is listed twice", clean)
		}
		seen[clean] = true
		fmt.Fprintf(&b, "  - path: %s\n    replica:\n", yamlString(clean))
		r := db.Replica
		switch r.Type {
		case "s3":
			if r.Bucket == "" {
				return nil, fmt.Errorf("snapshot: the s3 replica of %s has no bucket", clean)
			}
			b.WriteString("      type: s3\n")
			b.WriteString("      sync-interval: " + litestreamSyncInterval + "\n")
			fmt.Fprintf(&b, "      bucket: %s\n", yamlString(r.Bucket))
			if r.Path != "" {
				fmt.Fprintf(&b, "      path: %s\n", yamlString(r.Path))
			}
			if r.Endpoint != "" {
				fmt.Fprintf(&b, "      endpoint: %s\n", yamlString(r.Endpoint))
			}
			if r.Region != "" {
				fmt.Fprintf(&b, "      region: %s\n", yamlString(r.Region))
			}
			if r.ForcePathStyle {
				b.WriteString("      force-path-style: true\n")
			}
		case "file":
			if !filepath.IsAbs(r.Dir) {
				return nil, fmt.Errorf("snapshot: the file replica of %s needs an absolute folder, not %q", clean, r.Dir)
			}
			b.WriteString("      type: file\n")
			b.WriteString("      sync-interval: " + litestreamSyncInterval + "\n")
			fmt.Fprintf(&b, "      path: %s\n", yamlString(filepath.Clean(r.Dir)))
		default:
			return nil, fmt.Errorf("snapshot: unknown replica type %q for %s", r.Type, clean)
		}
	}
	return []byte(b.String()), nil
}

func (l *Litestream) logger() *slog.Logger {
	if l.Logger != nil {
		return l.Logger
	}
	return slog.Default()
}

func (l *Litestream) secrets() []string { return secretValues(l.Env) }

// writeConfig writes the configuration to a new file that only this user
// can read and returns its path.
func (l *Litestream) writeConfig() (string, error) {
	data, err := ConfigYAML(l.DBs)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(l.TempDir, "litestream-*.yml")
	if err != nil {
		return "", fmt.Errorf("snapshot: writing litestream's configuration: %w", err)
	}
	// CreateTemp makes the file 0600 already; Chmod says so where a
	// platform's default would differ.
	_ = f.Chmod(0o600)
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("snapshot: writing litestream's configuration: %w", err)
	}
	return f.Name(), nil
}

// command builds a litestream process. -no-expand-env keeps a "$" in a
// path literal: litestream would otherwise replace it from its environment.
func (l *Litestream) command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	if l.Binary == "" {
		return nil, errors.New("snapshot: no litestream binary configured")
	}
	cmd := exec.CommandContext(ctx, l.Binary, args...)
	cmd.Env = append([]string{}, l.Env...)
	cmd.Cancel = func() error { return terminate(cmd.Process) }
	cmd.WaitDelay = litestreamWaitDelay
	return cmd, nil
}

// Run replicates l.DBs until ctx ends, starting litestream again, after a
// backoff, whenever it exits. It returns nil once ctx ends and the child
// has stopped, and an error only when it cannot start at all (a bad
// configuration). One Run at a time per Litestream.
func (l *Litestream) Run(ctx context.Context) error {
	cfg, err := l.writeConfig()
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(cfg) }()
	minBackoff, maxBackoff := l.MinBackoff, l.MaxBackoff
	if minBackoff <= 0 {
		minBackoff = time.Second
	}
	if maxBackoff <= 0 {
		maxBackoff = time.Minute
	}
	maxBackoff = max(maxBackoff, minBackoff)
	backoff := minBackoff
	for first := true; ; first = false {
		if ctx.Err() != nil {
			return nil
		}
		started := time.Now()
		l.runChild(ctx, cfg, !first)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(started) >= restartReset {
			backoff = minBackoff
		}
		l.logger().Warn("litestream exited; restarting", "in", backoff, "error", l.Status().LastError)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		backoff = min(2*backoff, maxBackoff)
	}
}

// runChild runs one litestream until it exits or ctx ends and records
// what happened in the status.
func (l *Litestream) runChild(ctx context.Context, cfg string, restart bool) {
	secrets := l.secrets()
	out := &lineWriter{secrets: secrets, emit: l.emit}
	exited := func(err error) {
		out.flush()
		l.mu.Lock()
		defer l.mu.Unlock()
		l.status.Running, l.status.PID, l.status.Since = false, 0, time.Time{}
		l.status.LastExit = time.Now()
		if ctx.Err() != nil {
			return // stopped on purpose
		}
		msg := "litestream exited"
		if err != nil {
			msg += ": " + err.Error()
		}
		if last := out.lastError(); last != "" {
			msg += ": " + last
		}
		l.status.LastError = redact(msg, secrets)
	}
	if restart {
		l.mu.Lock()
		l.status.Restarts++
		l.mu.Unlock()
	}
	cmd, err := l.command(ctx, "replicate", "-no-expand-env", "-config", cfg)
	if err != nil {
		exited(err)
		return
	}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		exited(err)
		return
	}
	l.mu.Lock()
	l.status.Running, l.status.PID, l.status.Since = true, cmd.Process.Pid, time.Now()
	l.mu.Unlock()
	exited(cmd.Wait())
}

// emit passes on one redacted line of the child's output.
func (l *Litestream) emit(line string) {
	if l.Stderr != nil {
		_, _ = io.WriteString(l.Stderr, line+"\n")
		return
	}
	if isProblem(line) {
		l.logger().Warn("litestream", "line", line)
	} else {
		l.logger().Debug("litestream", "line", line)
	}
}

// isProblem reports whether a line of litestream's log (slog's text
// format) is a warning or an error.
func isProblem(line string) bool {
	return strings.Contains(line, "level=WARN") || strings.Contains(line, "level=ERROR") ||
		strings.HasPrefix(line, "Error:") || strings.HasPrefix(line, "error:")
}

// Status returns the state of replication. It is safe to call while Run runs.
func (l *Litestream) Status() LitestreamStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

// Restore writes the latest copy of dbPath, one of l.DBs, from its replica
// to out, which must not exist yet. The restore drill and tests use it to
// prove that the replica can be read back.
func (l *Litestream) Restore(ctx context.Context, dbPath, out string) error {
	known := false
	for _, db := range l.DBs {
		known = known || filepath.Clean(db.Path) == filepath.Clean(dbPath)
	}
	if !known {
		return fmt.Errorf("snapshot: %s is not one of the replicated databases", dbPath)
	}
	if _, err := os.Lstat(out); err == nil {
		return fmt.Errorf("snapshot: restore target %s exists already", out)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("snapshot: restore target: %w", err)
	}
	cfg, err := l.writeConfig()
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(cfg) }()
	cmd, err := l.command(ctx, "restore", "-no-expand-env", "-config", cfg, "-o", out, filepath.Clean(dbPath))
	if err != nil {
		return err
	}
	output := &tailBuffer{limit: 4 * stderrLimit}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = errors.Join(ctx.Err(), err)
		}
		msg := fmt.Sprintf("snapshot: litestream restore %s: %v", dbPath, err)
		if tail := redact(output.String(), l.secrets()); tail != "" {
			msg += ": " + tail
		}
		return errors.New(msg)
	}
	return nil
}

// lineWriter splits a child's output into lines, blanks secrets out of
// each and hands it to emit. It remembers the last line that reports a
// problem for the status. Output without a newline is passed on once it
// reaches lineLimit, so that a child cannot make it hold unbounded output,
// and redact keeps the last stderrLimit bytes of each line.
type lineWriter struct {
	secrets []string
	emit    func(string)

	mu      sync.Mutex
	partial []byte
	lastErr string
}

const lineLimit = 64 * 1024

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			w.partial = append(w.partial, p...)
			if len(w.partial) > lineLimit {
				w.line(w.partial)
				w.partial = w.partial[:0]
			}
			break
		}
		w.partial = append(w.partial, p[:i]...)
		w.line(w.partial)
		w.partial = w.partial[:0]
		p = p[i+1:]
	}
	return n, nil
}

// line handles one line; w.mu is held.
func (w *lineWriter) line(b []byte) {
	s := redact(strings.TrimRight(string(b), "\r"), w.secrets)
	if s == "" {
		return
	}
	if isProblem(s) {
		w.lastErr = s
	}
	w.emit(s)
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.partial) > 0 {
		w.line(w.partial)
		w.partial = w.partial[:0]
	}
}

func (w *lineWriter) lastError() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastErr
}
