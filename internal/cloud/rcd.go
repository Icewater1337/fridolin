package cloud

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RcloneDaemon is one long-lived `rclone rcd`, rclone's remote control
// daemon, that the stores it hands out (Store) send their operations to
// over its HTTP API. Every rclone process logs in to Backblaze B2 first
// (b2_authorize_account, a Class C transaction, capped at 2,500 a day on a
// free account); one daemon logs in once per remote and keeps the token,
// which B2 lets it use for 24 hours, so an operation costs only what it
// does: a stat one Class B HEAD, a download two Class B, an upload nothing
// capped, a listing one Class C per thousand objects.
//
// The daemon listens on a unix socket in a new 0700 folder of its own,
// with a random user and password made for each start, which reach rclone
// in its environment only (RCLONE_RC_USER, RCLONE_RC_PASS). Its remotes
// are configured as Rclone's are, by Env alone, with no config file. It
// starts on first use and again on the next operation after it died;
// Close stops it.
type RcloneDaemon struct {
	Binary string   // path of the rclone executable
	Env    []string // the daemon's environment, with the RCLONE_CONFIG_* variables
	// Secrets are extra strings to blank out of error messages, on top of
	// the values of Env's RCLONE_CONFIG_* variables.
	Secrets []string
	// Usage, when set, counts the B2 transactions of every operation.
	Usage *Usage
	// StartTimeout bounds how long a start may take to answer (default 30s).
	StartTimeout time.Duration
	// Op is prefixed to the operation names Usage counts ("vault",
	// "lease"); "" is "rclone".
	Op string

	mu     sync.Mutex
	proc   *rcProc
	closed bool
	// fsSince is when each remote ("fs" string) was first used by the
	// running daemon: its log-in, and again every day after, which is
	// when B2 makes rclone log in anew.
	fsSince map[string]time.Time
}

// rcProc is one running rclone rcd.
type rcProc struct {
	cmd    *exec.Cmd
	dir    string
	sock   string
	user   string
	pass   string
	client *http.Client
	stderr *tailBuffer
	done   chan struct{}
}

// daemonAuthCost is what rclone pays when it first uses a B2 remote:
// b2_authorize_account (Class C) and a check whether the remote's root is
// a file (a HEAD, Class B).
var daemonAuthCost = Cost{B: 1, C: 1}

// b2TokenLife is how long B2 honours an authorization token; rclone logs
// in again after it (Usage counts that).
const b2TokenLife = 24 * time.Hour

// Store returns the ObjectStore of remote (a remote and root, such as
// "vaultcrypt:" or "b2remote:bucket") on this daemon.
func (d *RcloneDaemon) Store(remote string) *RcloneRC {
	return &RcloneRC{d: d, Remote: remote}
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// running returns the running daemon, starting one when there is none.
func (d *RcloneDaemon) running(ctx context.Context) (*rcProc, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, errors.New("cloud: the rclone daemon is closed")
	}
	if p := d.proc; p != nil {
		select {
		case <-p.done:
			p.cleanup()
			d.proc = nil
		default:
			return p, nil
		}
	}
	p, err := d.start(ctx)
	if err != nil {
		return nil, err
	}
	d.proc = p
	d.fsSince = map[string]time.Time{}
	return p, nil
}

// start runs a new rclone rcd and waits until it answers.
func (d *RcloneDaemon) start(ctx context.Context) (*rcProc, error) {
	if d.Binary == "" {
		return nil, errors.New("cloud: no rclone binary configured")
	}
	dir, err := os.MkdirTemp("", "fridolin-rc-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	p := &rcProc{dir: dir, sock: filepath.Join(dir, "rc.sock"), stderr: &tailBuffer{limit: 4 * stderrLimit}, done: make(chan struct{})}
	if p.user, err = randomHex(16); err == nil {
		p.pass, err = randomHex(32)
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	// --config "" is rclone's in-memory configuration: no file is read or
	// written. --rc-serve serves objects at /[remote:]/key, which is how
	// a download streams. Remotes stay cached (and logged in) for as long
	// as the daemon runs, not rclone's default five idle minutes. The
	// server timeouts let a long upload or download finish.
	cmd := exec.Command(d.Binary, "rcd", "--config", "", "-q",
		"--rc-addr", "unix://"+filepath.ToSlash(p.sock), "--rc-serve",
		"--fs-cache-expire-duration", "87600h",
		"--rc-server-read-timeout", "24h", "--rc-server-write-timeout", "24h")
	cmd.Env = append(append([]string{}, d.Env...), "RCLONE_RC_USER="+p.user, "RCLONE_RC_PASS="+p.pass)
	cmd.Stderr = p.stderr
	setDaemonAttrs(cmd)
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("cloud: starting rclone rcd: %w", err)
	}
	p.cmd = cmd
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	p.client = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", p.sock)
		},
		MaxIdleConns:        4,
		IdleConnTimeout:     time.Minute,
		DisableCompression:  true,
		TLSHandshakeTimeout: time.Second,
	}}
	timeout := d.StartTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		err := p.call(ctx, "rc/noop", map[string]any{}, nil)
		if err == nil {
			return p, nil
		}
		select {
		case <-p.done:
			p.cleanup()
			return nil, &RcloneError{Op: "rcd", ExitCode: cmd.ProcessState.ExitCode(), Stderr: d.redact(p.stderr.String()),
				Err: errors.New("rclone rcd exited at start")}
		default:
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			p.stop()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, &RcloneError{Op: "rcd", ExitCode: -1, Stderr: d.redact(p.stderr.String()),
				Err: fmt.Errorf("rclone rcd did not answer within %s: %v", timeout, err)}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stop ends the daemon and removes its folder.
func (p *rcProc) stop() {
	if p.cmd != nil && p.cmd.Process != nil {
		_ = terminateProcess(p.cmd.Process)
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	}
	p.cleanup()
}

func (p *rcProc) cleanup() {
	if p.client != nil {
		p.client.CloseIdleConnections()
	}
	_ = os.RemoveAll(p.dir)
}

// Close stops the daemon. Stores of a closed daemon fail.
func (d *RcloneDaemon) Close() error {
	d.mu.Lock()
	p := d.proc
	d.proc, d.closed = nil, true
	d.mu.Unlock()
	if p != nil {
		p.stop()
	}
	return nil
}

// redact blanks the secrets of d out of s and keeps the last stderrLimit
// bytes.
func (d *RcloneDaemon) redact(s string) string {
	return (&Rclone{Env: d.Env, Secrets: d.Secrets}).redact(s)
}

// rcError is the error body of rclone's remote control.
type rcError struct {
	Status int    `json:"status"`
	Msg    string `json:"error"`
}

func (e *rcError) Error() string { return e.Msg }

// call posts in to method and decodes the answer into out (when not nil).
func (p *rcProc) call(ctx context.Context, method string, in any, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://rclone/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return p.do(req, out)
}

// do sends req with the daemon's credentials and decodes a JSON answer.
func (p *rcProc) do(req *http.Request, out any) error {
	req.SetBasicAuth(p.user, p.pass)
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		e := &rcError{Status: resp.StatusCode}
		if json.Unmarshal(data, e) != nil || e.Msg == "" {
			e.Msg = strings.TrimSpace(string(data))
			if e.Msg == "" {
				e.Msg = resp.Status
			}
		}
		e.Status = resp.StatusCode
		return e
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// charge counts op's cost, and the remote's log-in when this is its first
// use by the running daemon or its token has expired since.
func (d *RcloneDaemon) charge(fs, op string, cost Cost) {
	if d.Usage == nil {
		return
	}
	prefix := d.Op
	if prefix == "" {
		prefix = "rclone"
	}
	d.mu.Lock()
	since, ok := d.fsSince[fs]
	now := time.Now()
	if !ok || now.Sub(since) >= b2TokenLife {
		if d.fsSince == nil {
			d.fsSince = map[string]time.Time{}
		}
		d.fsSince[fs] = now
		d.mu.Unlock()
		d.Usage.AddCost(prefix+":authorize", daemonAuthCost)
	} else {
		d.mu.Unlock()
	}
	d.Usage.AddCost(prefix+":"+op, cost)
}

// putBufferLimit is the largest body Put reads whole before it sends it;
// rclone's streaming cutoff is 100 KiB, so anything longer streams there.
const putBufferLimit = 4 << 20

// RcloneRC is an ObjectStore on an RcloneDaemon: operations/stat (Stat),
// a GET of the object served at /[remote]/key (Get),
// operations/uploadfile (Put), operations/list (List) and
// operations/deletefile (Delete).
type RcloneRC struct {
	d      *RcloneDaemon
	Remote string
}

// Daemon is the daemon the store talks to.
func (r *RcloneRC) Daemon() *RcloneDaemon { return r.d }

// Close stops the store's daemon (and with it every store on it).
func (r *RcloneRC) Close() error { return r.d.Close() }

// SetUsage makes the store's daemon count its transactions in u.
func (r *RcloneRC) SetUsage(u *Usage, op string) {
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	r.d.Usage, r.d.Op = u, op
}

// fail turns an error of the daemon into the store's error.
func (r *RcloneRC) fail(op, key string, err error) error {
	if err == nil {
		return nil
	}
	e := &RcloneError{Op: op, Key: key, ExitCode: -1, Err: err}
	var re *rcError
	if errors.As(err, &re) {
		e.Stderr = r.d.redact(re.Msg)
		e.Err = fmt.Errorf("rclone rcd answered %d", re.Status)
		if re.Status == http.StatusNotFound {
			e.ExitCode = rcloneExitFileNotFound
		}
	} else {
		e.Stderr = r.d.redact(err.Error())
	}
	var out error = e
	if IsCapExceeded(e) {
		out = &CapError{Err: e}
	}
	if u := r.d.Usage; u != nil {
		u.NoteError(out)
	}
	return out
}

// proc returns the running daemon, starting one when needed, and counts
// op's cost (with the remote's log-in, when this is its first use).
func (r *RcloneRC) proc(ctx context.Context, op string, cost Cost) (*rcProc, error) {
	if r.Remote == "" {
		return nil, errors.New("cloud: no rclone remote configured")
	}
	p, err := r.d.running(ctx)
	if err != nil {
		return nil, err
	}
	r.d.charge(r.Remote, op, cost)
	return p, nil
}

// call runs one remote-control method of op.
func (r *RcloneRC) call(ctx context.Context, op string, cost Cost, method string, in any, out any) error {
	p, err := r.proc(ctx, op, cost)
	if err != nil {
		return err
	}
	return p.call(ctx, method, in, out)
}

type rcItem struct {
	Path    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// Stat implements ObjectStore with operations/stat. filesOnly keeps
// rclone from listing the parent folder when there is no such object.
func (r *RcloneRC) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := ValidKey(key); err != nil {
		return ObjectInfo{}, err
	}
	var out struct {
		Item *rcItem `json:"item"`
	}
	err := r.call(ctx, "stat", Cost{B: 1}, "operations/stat", map[string]any{"fs": r.Remote, "remote": key,
		"opt": map[string]any{"filesOnly": true, "noMimeType": true}}, &out)
	if err != nil {
		err = r.fail("stat", key, err)
		if re, ok := errors.AsType[*RcloneError](err); ok && re.notFound() {
			return ObjectInfo{}, fmt.Errorf("cloud: stat %s: %w", key, ErrNotFound)
		}
		return ObjectInfo{}, err
	}
	if out.Item == nil || out.Item.IsDir {
		return ObjectInfo{}, fmt.Errorf("cloud: stat %s: %w", key, ErrNotFound)
	}
	return ObjectInfo{Key: key, Size: out.Item.Size, ModTime: out.Item.ModTime}, nil
}

// serveURL is where --rc-serve serves key of r's remote.
func (r *RcloneRC) serveURL(key string) string {
	u := url.URL{Scheme: "http", Host: "rclone", Path: "/[" + r.Remote + "]/" + key}
	return u.String()
}

// Get implements ObjectStore with one GET of the served object: a missing
// one is a 404, and a transfer that breaks off is a short body, which
// net/http reports as an error since the length is known.
func (r *RcloneRC) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ValidKey(key); err != nil {
		return nil, err
	}
	p, err := r.proc(ctx, "get", Cost{B: 2})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.serveURL(key), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(p.user, p.pass)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, r.fail("get", key, err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp.Body, nil
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("cloud: get %s: %w", key, ErrNotFound)
	}
	e := &rcError{Status: resp.StatusCode}
	if json.Unmarshal(data, e) != nil || e.Msg == "" {
		e.Msg = strings.TrimSpace(string(data))
	}
	e.Status = resp.StatusCode
	return nil, r.fail("get", key, e)
}

// Put implements ObjectStore with operations/uploadfile, which streams the
// multipart body into the object as rclone rcat does. A body that fails
// part way stores nothing: one up to putBufferLimit is read whole before
// anything is sent, and a longer one aborts the request, so rclone sees a
// broken stream, which it cancels (on B2 a large-file upload that is not
// finished is never an object; a backend that writes in place, like a
// local folder, removes the partial file a moment after Put returned).
func (r *RcloneRC) Put(ctx context.Context, key string, body io.Reader) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	// rclone uploads a body shorter than its streaming cutoff in one
	// piece, and takes a body that breaks off there for a short file. So
	// a body up to putBufferLimit is read whole before anything is sent;
	// only a longer one streams, and rclone aborts a stream that breaks.
	head := make([]byte, putBufferLimit+1)
	var n int
	read := make(chan error, 1)
	go func() {
		// Not io.ReadFull: it reports a short body as ErrUnexpectedEOF,
		// which a broken reader may return itself. Only io.EOF ends one.
		var err error
		for n < len(head) && err == nil {
			var k int
			k, err = body.Read(head[n:])
			n += k
		}
		read <- err
	}()
	var err error
	select {
	case err = <-read:
	case <-ctx.Done():
		return ctx.Err()
	}
	switch {
	case errors.Is(err, io.EOF):
		body = bytes.NewReader(head[:n])
	case err != nil:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("cloud: rclone upload %s: reading the body: %w", key, err)
	default:
		body = io.MultiReader(bytes.NewReader(head), body)
	}
	p, err := r.proc(ctx, "put", Cost{A: 1})
	if err != nil {
		return err
	}
	dir, name := path.Split(key)
	q := url.Values{"fs": {r.Remote}, "remote": {strings.TrimSuffix(dir, "/")}}
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	copied := make(chan error, 1)
	go func() {
		part, err := mw.CreateFormFile("file", name)
		if err == nil {
			_, err = io.Copy(part, body)
		}
		if err == nil {
			err = mw.Close()
		}
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		copied <- err
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://rclone/operations/uploadfile?"+q.Encode(), pr)
	if err != nil {
		_ = pr.CloseWithError(err)
		<-copied
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	err = p.do(req, nil)
	_ = pr.CloseWithError(errors.New("cloud: upload ended"))
	cerr := <-copied
	if cerr != nil && ctx.Err() == nil {
		return fmt.Errorf("cloud: rclone upload %s: reading the body: %w", key, cerr)
	}
	if err != nil {
		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), r.fail("put", key, err))
		}
		return r.fail("put", key, err)
	}
	return nil
}

// List implements ObjectStore with operations/list, recursive, files only.
func (r *RcloneRC) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	if err := validPrefix(prefix); err != nil {
		return nil, err
	}
	var out struct {
		List []rcItem `json:"list"`
	}
	p, err := r.proc(ctx, "list", Cost{})
	if err == nil {
		err = p.call(ctx, "operations/list", map[string]any{"fs": r.Remote, "remote": prefix,
			"opt": map[string]any{"recurse": true, "filesOnly": true, "noMimeType": true}}, &out)
		// A listing is one Class C per thousand names, and a HEAD of the
		// folder's name first.
		r.d.charge(r.Remote, "list", Cost{B: 1, C: float64(max(1, (len(out.List)+999)/1000))})
	}
	if err != nil {
		err = r.fail("list", prefix, err)
		if re, ok := errors.AsType[*RcloneError](err); ok && re.notFound() {
			return nil, nil
		}
		return nil, err
	}
	list := make([]ObjectInfo, 0, len(out.List))
	for _, e := range out.List {
		key := e.Path
		if prefix != "" && !strings.HasPrefix(key, prefix+"/") {
			key = path.Join(prefix, e.Path)
		}
		list = append(list, ObjectInfo{Key: key, Size: e.Size, ModTime: e.ModTime})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	return list, nil
}

// Delete implements ObjectStore with operations/deletefile.
func (r *RcloneRC) Delete(ctx context.Context, key string) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	err := r.call(ctx, "delete", Cost{A: 1, B: 1}, "operations/deletefile", map[string]any{"fs": r.Remote, "remote": key}, nil)
	if err == nil {
		return nil
	}
	err = r.fail("delete", key, err)
	if re, ok := errors.AsType[*RcloneError](err); ok && re.notFound() {
		return nil
	}
	return err
}
