package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// Rclone is an ObjectStore that runs the rclone binary. Each operation is
// one rclone process: rcat (Put), cat (Get), lsjson (Stat, List) and
// deletefile (Delete).
//
// The remote is configured only through Env (RemoteEnv, B2Env, CryptEnv,
// VaultRemote.Env); rclone is told to use no config file at all, so a
// stray rclone.conf can neither leak into nor override the vault's remote.
// Env is the child's whole environment: nothing of this process's
// environment is passed on unless the caller includes it.
type Rclone struct {
	Binary string   // path of the rclone executable
	Remote string   // the remote and root, such as "vaultcrypt:" or "local:/x"
	Env    []string // the child's environment, with the RCLONE_CONFIG_* variables
	// Secrets are extra strings to blank out of error messages, on top of
	// the values of Env's RCLONE_CONFIG_* variables.
	Secrets []string
	// WaitDelay bounds how long a cancelled rclone may take to exit after
	// it is killed before its pipes are closed (default 5s).
	WaitDelay time.Duration
	// Usage, when set, counts the B2 transactions of every run; Op is
	// prefixed to the operation names ("" is "rclone").
	Usage *Usage
	Op    string
}

// rcloneRunCost is what every rclone process pays on B2 before its
// operation: b2_authorize_account (Class C) and the check whether the
// path it was given is a file (a HEAD, Class B). RcloneDaemon pays it once.
var rcloneRunCost = Cost{B: 1, C: 1}

// SetUsage makes r count its transactions in u.
func (r *Rclone) SetUsage(u *Usage, op string) { r.Usage, r.Op = u, op }

// charge counts one rclone run of op.
func (r *Rclone) charge(op string, cost Cost) {
	r.Usage.AddCost(r.opName(op), rcloneRunCost.Plus(cost))
}

func (r *Rclone) opName(op string) string {
	if r.Op == "" {
		return "rclone:" + op
	}
	return r.Op + ":" + op
}

// rcloneExitDirNotFound and rcloneExitFileNotFound are rclone's exit codes
// for a missing directory and a missing file.
const (
	rcloneExitDirNotFound  = 3
	rcloneExitFileNotFound = 4
)

// stderrLimit is how much of rclone's error output an error keeps.
const stderrLimit = 2048

// RcloneError is a failed rclone run.
type RcloneError struct {
	Op       string // the rclone command, such as "rcat"
	Key      string
	ExitCode int    // -1 when rclone did not exit by itself
	Stderr   string // the tail of rclone's error output, secrets blanked
	Err      error
}

func (e *RcloneError) Error() string {
	msg := fmt.Sprintf("cloud: rclone %s %s: %v", e.Op, e.Key, e.Err)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

func (e *RcloneError) Unwrap() error { return e.Err }

// notFound reports whether rclone said the path does not exist.
func (e *RcloneError) notFound() bool {
	if e.ExitCode == rcloneExitDirNotFound || e.ExitCode == rcloneExitFileNotFound {
		return true
	}
	s := strings.ToLower(e.Stderr)
	return strings.Contains(s, "object not found") || strings.Contains(s, "file not found") ||
		strings.Contains(s, "directory not found")
}

func (r *Rclone) target(key string) string {
	if key == "" {
		return r.Remote
	}
	if strings.HasSuffix(r.Remote, ":") || strings.HasSuffix(r.Remote, "/") {
		return r.Remote + key
	}
	return r.Remote + "/" + key
}

// command builds an rclone process for args. Every run gets the global
// flags that pin it to the env-defined remote.
func (r *Rclone) command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	if r.Binary == "" {
		return nil, errors.New("cloud: no rclone binary configured")
	}
	if r.Remote == "" {
		return nil, errors.New("cloud: no rclone remote configured")
	}
	// --config "" is rclone's in-memory configuration: no file is read or
	// written. -q keeps only errors on stderr.
	full := append([]string{"--config", "", "-q"}, args...)
	cmd := exec.CommandContext(ctx, r.Binary, full...)
	cmd.Env = append([]string{}, r.Env...)
	cmd.WaitDelay = r.WaitDelay
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 5 * time.Second
	}
	return cmd, nil
}

// redact blanks the secrets of r out of s and keeps the last stderrLimit
// bytes.
func (r *Rclone) redact(s string) string {
	for _, kv := range r.Env {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || val == "" || !strings.HasPrefix(name, "RCLONE_CONFIG_") ||
			strings.HasSuffix(name, "_TYPE") || strings.HasSuffix(name, "_REMOTE") {
			continue
		}
		s = strings.ReplaceAll(s, val, "***")
	}
	for _, secret := range r.Secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "***")
		}
	}
	s = strings.TrimSpace(s)
	if len(s) > stderrLimit {
		s = "…" + s[len(s)-stderrLimit:]
	}
	return s
}

func (r *Rclone) runError(ctx context.Context, op, key string, err error, stderr string) error {
	e := &RcloneError{Op: op, Key: key, ExitCode: -1, Stderr: r.redact(stderr), Err: err}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		e.ExitCode = exit.ExitCode()
	}
	if ctx.Err() != nil {
		e.Err = errors.Join(ctx.Err(), err)
	}
	if IsCapExceeded(e) {
		ce := &CapError{Err: e}
		r.Usage.NoteError(ce)
		return ce
	}
	return e
}

// run runs rclone to completion and returns its standard output.
func (r *Rclone) run(ctx context.Context, op, key string, args ...string) ([]byte, error) {
	cmd, err := r.command(ctx, append([]string{op}, args...)...)
	if err != nil {
		return nil, err
	}
	var stdout bytes.Buffer
	stderr := &tailBuffer{limit: 4 * stderrLimit}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return nil, r.runError(ctx, op, key, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// Put implements ObjectStore with `rclone rcat`. It feeds rclone's input
// itself rather than through exec, so that a cancelled Put returns even
// while body blocks, and a body that fails part way kills rclone before
// it could see an end of input and store a truncated object.
func (r *Rclone) Put(ctx context.Context, key string, body io.Reader) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.charge("put", Cost{A: 1})
	cmd, err := r.command(runCtx, "rcat", r.target(key))
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stderr := &tailBuffer{limit: 4 * stderrLimit}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return r.runError(ctx, "rcat", key, err, "")
	}
	copied := make(chan error, 1)
	go func() {
		_, err := io.Copy(stdin, body)
		if err != nil {
			cancel() // kill first: closing stdin would end the object here
		}
		_ = stdin.Close()
		copied <- err
	}()
	if err := cmd.Wait(); err != nil {
		select {
		case cerr := <-copied:
			if cerr != nil && ctx.Err() == nil {
				return fmt.Errorf("cloud: rclone rcat %s: reading the body: %w", key, cerr)
			}
		default:
		}
		return r.runError(ctx, "rcat", key, err, stderr.String())
	}
	// rclone ends well only after it read the end of its input, which the
	// copy sends once it is done.
	if err := <-copied; err != nil {
		return fmt.Errorf("cloud: rclone rcat %s: reading the body: %w", key, err)
	}
	return nil
}

// Get implements ObjectStore with `rclone cat`. It checks that the object
// exists first, because cat of a missing path prints nothing and succeeds.
func (r *Rclone) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if _, err := r.Stat(ctx, key); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	// cat lists the object's folder to find it, then reads it.
	r.charge("get", Cost{B: 2, C: 1})
	cmd, err := r.command(runCtx, "cat", r.target(key))
	if err != nil {
		cancel()
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr := &tailBuffer{limit: 4 * stderrLimit}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, r.runError(ctx, "cat", key, err, "")
	}
	return &catReader{r: r, ctx: ctx, key: key, cmd: cmd, out: out, stderr: stderr, cancel: cancel}, nil
}

// catReader streams rclone cat's output. Reaching the end waits for rclone
// and reports its failure, so a transfer that broke off is an error and
// not a short file.
type catReader struct {
	r      *Rclone
	ctx    context.Context
	key    string
	cmd    *exec.Cmd
	out    io.ReadCloser
	stderr *tailBuffer
	cancel context.CancelFunc

	once     sync.Once
	mu       sync.Mutex
	finished bool // wait has returned
	waitErr  error
}

func (c *catReader) wait() error {
	c.once.Do(func() {
		if err := c.cmd.Wait(); err != nil {
			c.waitErr = c.r.runError(c.ctx, "cat", c.key, err, c.stderr.String())
		}
		c.cancel()
		c.mu.Lock()
		c.finished = true
		c.mu.Unlock()
	})
	return c.waitErr
}

func (c *catReader) Read(p []byte) (int, error) {
	n, err := c.out.Read(p)
	if errors.Is(err, io.EOF) {
		if werr := c.wait(); werr != nil {
			return n, werr
		}
	}
	return n, err
}

// Close stops rclone if it is still running. Closing before the end is a
// cancellation, not an error; closing after it reports what Read did.
func (c *catReader) Close() error {
	c.mu.Lock()
	finished := c.finished
	c.mu.Unlock()
	if finished {
		return c.waitErr
	}
	c.cancel()
	_ = c.wait()
	return nil
}

// lsjsonEntry is one item of rclone lsjson's output.
type lsjsonEntry struct {
	Path    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// Stat implements ObjectStore with `rclone lsjson --stat`.
func (r *Rclone) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := ValidKey(key); err != nil {
		return ObjectInfo{}, err
	}
	r.charge("stat", Cost{B: 1})
	out, err := r.run(ctx, "lsjson", key, "--stat", "--no-mimetype", r.target(key))
	if err != nil {
		var re *RcloneError
		if errors.As(err, &re) && re.notFound() {
			// Not an object: lsjson --stat lists the parent folder for a
			// directory of that name.
			r.Usage.Add(ClassC, r.opName("stat"), 1)
			return ObjectInfo{}, fmt.Errorf("cloud: stat %s: %w", key, ErrNotFound)
		}
		return ObjectInfo{}, err
	}
	var e lsjsonEntry
	if err := json.Unmarshal(out, &e); err != nil {
		return ObjectInfo{}, fmt.Errorf("cloud: rclone lsjson --stat %s: %w", key, err)
	}
	if e.IsDir {
		return ObjectInfo{}, fmt.Errorf("cloud: stat %s: is a directory: %w", key, ErrNotFound)
	}
	return ObjectInfo{Key: key, Size: e.Size, ModTime: e.ModTime}, nil
}

// List implements ObjectStore with `rclone lsjson -R --files-only`.
func (r *Rclone) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	if err := validPrefix(prefix); err != nil {
		return nil, err
	}
	out, err := r.run(ctx, "lsjson", prefix, "-R", "--files-only", "--no-mimetype", r.target(prefix))
	if err != nil {
		var re *RcloneError
		if errors.As(err, &re) && re.notFound() {
			return nil, nil
		}
		return nil, err
	}
	var entries []lsjsonEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("cloud: rclone lsjson %s: %w", prefix, err)
	}
	r.charge("list", Cost{B: 1, C: float64(max(1, (len(entries)+999)/1000))})
	list := make([]ObjectInfo, 0, len(entries))
	for _, e := range entries {
		key := e.Path
		if prefix != "" {
			key = path.Join(prefix, e.Path)
		}
		list = append(list, ObjectInfo{Key: key, Size: e.Size, ModTime: e.ModTime})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	return list, nil
}

// Delete implements ObjectStore with `rclone deletefile`.
func (r *Rclone) Delete(ctx context.Context, key string) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	r.charge("delete", Cost{A: 1, B: 1})
	_, err := r.run(ctx, "deletefile", key, r.target(key))
	var re *RcloneError
	if errors.As(err, &re) && re.notFound() {
		return nil
	}
	return err
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
