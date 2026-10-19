package main

import "github.com/Icewater1337/fridolin/internal/app"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/registry"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// testChildEnv is what the programs the bridge starts get in tests on top
// of childEnvAllowlist: the FRIDOLIN_FAKE_* variables that steer the fakes, this
// binary playing the Claude or Plaud CLI among them, and GORACE, which
// keeps each run of this binary from waiting a second on exit under -race.
var testChildEnv = []childEnvVar{
	{"FRIDOLIN_FAKE_*", anyChild},
	{"GORACE", anyChild},
}

// TestRestartInATestFailsTheRun: a restart runs the bridge again in place
// (serve), which in a test is the test binary. The guards of POST
// /api/restart stop a request before it asks for one; and should a test
// restart a bridge through serve without replacing execSelf, the run must
// fail rather than end as the fake CLI with exit 0, which go test would
// report as a pass of the whole package (reexecutedInPlace). Both are
// checked in a test binary of its own, since the restart replaces the
// process that makes it.
func TestRestartInATestFailsTheRun(t *testing.T) {
	const child, held = "FRIDOLIN_TEST_RESTART_CHILD", "the guards held: this is still the test"
	if os.Getenv(child) == "1" {
		tb := newTestBridge(t, `, "http_listen": "127.0.0.1:0"`)
		ctx, cancel := context.WithCancelCause(context.Background())
		tb.restart = func() { cancel(app.ErrRestart) }
		go serve(ctx, tb.Bridge, func() bool { return false })
		for _, c := range []struct {
			name   string
			mutate func(*http.Request)
			want   int
		}{
			{"without the token", func(r *http.Request) { r.Header.Del("X-Bridge-Token") }, http.StatusUnauthorized},
			{"without X-Bridge-Dashboard", func(r *http.Request) { r.Header.Del("X-Bridge-Dashboard") }, http.StatusForbidden},
			{"a form body", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType},
			{"from another site", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.com") }, http.StatusForbidden},
		} {
			if w := tb.do("POST", "/api/restart", map[string]any{}, c.mutate); w.Code != c.want {
				t.Errorf("%s: %d, want %d", c.name, w.Code, c.want)
			}
		}
		if ctx.Err() != nil {
			t.Fatalf("a refused POST /api/restart asked for a restart: %v", context.Cause(ctx))
		}
		fmt.Println(held)
		if w := tb.do("POST", "/api/restart", nil); w.Code != http.StatusAccepted {
			t.Fatalf("POST /api/restart: %d %s", w.Code, w.Body.String())
		}
		time.Sleep(10 * time.Second)
		t.Fatal("the restart did not re-execute the process")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRestartInATestFailsTheRun$", "-test.count=1", "-test.v")
	// The child's temp folders inside this test's: replaced by the restart,
	// it never removes its own.
	cmd.Env = append(os.Environ(), child+"=1", "GORACE=atexit_sleep_ms=0", "TMPDIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case !strings.Contains(string(out), held):
		t.Fatalf("a probe of POST /api/restart got through its guards (%v):\n%s", err, out)
	case !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(out), "re-executed the bridge"):
		t.Fatalf("the test binary, re-executed by the restart, ended with %v instead of failing the run:\n%s", err, out)
	}
}

// testHome is the HOME the tests start with.
var testHome string

// goToolHome is the HOME the tests were started with, for the go command
// TestSmoke builds the bridge with: its build and module caches are there
// by default. Nothing else may use it.
var goToolHome string

// pkgDir is the package directory the tests were started in, whose
// testdata is the tests' own (see linkTestdata).
var pkgDir string

// repoDir is the checkout: the folder of the go.mod above pkgDir. The
// tests run elsewhere, so a test that reads a file of the checkout joins
// it to this.
var repoDir string

// loggedPrompts returns the prompts the fake CLI logged to path, by the
// text of their new message.
func loggedPrompts(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range fakePromptsIn(t, path) {
		p = strings.TrimSuffix(p, "\n")
		out[testutil.NewMessage(p)] = p
	}
	return out
}

// fakeConnect connects to the unix socket p, as a brain's tool would, for
// the sandbox tests: it exits 0 when it could, and says when the sandbox
// refused, in the words sandboxRun looks for.
func fakeConnect(p string) int {
	c, err := net.Dial("unix", p)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			fmt.Println("connect: Operation not permitted")
		} else {
			fmt.Println("connect:", err)
		}
		return 1
	}
	c.Close()
	return 0
}

// fakeSession prints the process's pid and the session it is in: a process
// that leads a session of its own prints the same number twice.
func fakeSession() int {
	// syscall has no Getsid on Linux.
	sid, _, errno := syscall.Syscall(syscall.SYS_GETSID, 0, 0, 0)
	if errno != 0 {
		fmt.Println("getsid:", errno)
		return 1
	}
	fmt.Printf("pid=%d sid=%d\n", os.Getpid(), sid)
	return 0
}

// fakeTTY tries what a run could do to the terminal of the bridge that
// started it, and prints how each went: open /dev/tty for reading, push a
// character into its input with TIOCSTI (which only needs reading), and open
// it for writing.
func fakeTTY() int {
	f, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		fmt.Println("read:", err)
	} else {
		fmt.Println("read: ok")
		c := byte('#')
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCSTI), uintptr(unsafe.Pointer(&c))); errno != 0 {
			fmt.Println("inject:", errno)
		} else {
			fmt.Println("inject: ok")
		}
		f.Close()
	}
	if w, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err != nil {
		fmt.Println("write:", err)
	} else {
		fmt.Println("write: ok")
		w.Close()
	}
	return 0
}

// fakeTTYParent plays the bridge in a terminal: it checks that it has one,
// then runs the command spec describes (JSON: argv, dir, env, and isolate
// for platform.IsolateProcessGroup, as a brain's run gets it) and prints
// its output.
func fakeTTYParent(spec string) int {
	var s struct {
		Argv, Env []string
		Dir       string
		Isolate   bool
	}
	if err := json.Unmarshal([]byte(spec), &s); err != nil || len(s.Argv) == 0 {
		fmt.Println("bad spec:", err)
		return 1
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		fmt.Println("the parent has no terminal:", err)
		return 1
	}
	tty.Close()
	// A run in the terminal's background process group that tries TIOCSTI
	// is stopped (SIGTTOU), so it must not wait for that run forever.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Argv[0], s.Argv[1:]...)
	cmd.Dir, cmd.Env = s.Dir, s.Env
	if s.Isolate {
		platform.IsolateProcessGroup(cmd)
	}
	out, err := cmd.CombinedOutput()
	os.Stdout.Write(out)
	if err != nil {
		fmt.Println("child:", err)
		return 1
	}
	return 0
}

// fakePromptsIn returns the prompts the fake CLI logged to path, oldest
// first.
func fakePromptsIn(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(string(data), testutil.PromptMark+"\n")[1:]
}

func fakeClaudeEnv(t *testing.T, sleepMS int) string {
	t.Helper()
	t.Setenv("FRIDOLIN_FAKE_CLAUDE", "1")
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", strconv.Itoa(sleepMS))
	return os.Args[0]
}

// requireTestDataDir fails the test unless every path the bridge writes its
// data to lies inside this test's temporary directory. The helpers that open
// a store call it first, so a config that still points at a real install
// (a data_dir copied from one, say) stops the test before the store, the
// token or rclone's config is touched.
func requireTestDataDir(t testing.TB, cfg *config.File) {
	t.Helper()
	// t.TempDir hands out numbered folders inside one directory per test.
	if err := checkTestDataDir(filepath.Dir(t.TempDir()), cfg); err != nil {
		t.Fatal(err)
	}
}

// checkTestDataDir reports the first data path of cfg outside root. Paths
// may use root as given or with its symlinks resolved (/var and /private/var
// on macOS).
func checkTestDataDir(root string, cfg *config.File) error {
	roots := []string{filepath.Clean(root), fsx.Canonical(root)}
	for _, p := range []struct{ field, path string }{
		{"data_dir", cfg.DataDir},
		{"db_path", cfg.DBPath},
		{"media_dir", cfg.MediaDir},
	} {
		inside := false
		for _, r := range roots {
			inside = inside || fsx.Within(p.path, r)
		}
		if !inside {
			return fmt.Errorf("%s %q is outside the test's temp dir %s: refusing to touch it", p.field, p.path, root)
		}
	}
	return nil
}

func TestCheckTestDataDir(t *testing.T) {
	root := t.TempDir()
	in := filepath.Join(root, "data")
	out := filepath.Join(filepath.Dir(root), "elsewhere")
	tests := []struct {
		name            string
		data, db, media string
		wantErr         string // "" means the paths are accepted
	}{
		{"all inside", in, in + "/bridge.db", in + "/media", ""},
		{"inside through the resolved root", fsx.Canonical(root) + "/data", fsx.Canonical(root) + "/data/bridge.db", fsx.Canonical(root) + "/data/media", ""},
		{"data dir outside", out, in + "/bridge.db", in + "/media", "data_dir"},
		{"database outside", in, out + "/bridge.db", in + "/media", "db_path"},
		{"media outside", in, in + "/bridge.db", out + "/media", "media_dir"},
		{"sibling sharing the prefix", root + "x/data", in + "/bridge.db", in + "/media", "data_dir"},
		{"relative to the working directory", "data", in + "/bridge.db", in + "/media", "data_dir"},
		{"empty", "", in + "/bridge.db", in + "/media", "data_dir"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkTestDataDir(root, &config.File{DataDir: tc.data, DBPath: tc.db, MediaDir: tc.media})
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want one about %s", err, tc.wantErr)
			}
		})
	}
}

func TestRequireTestDataDir(t *testing.T) {
	// TestCheckTestDataDir passing says nothing about whether the guard the
	// helpers call checks at all, or against which root. A data dir in
	// another test's temp dir lies in the system temp dir too, so a root
	// wider than the test's own lets it through.
	other := filepath.Join(t.TempDir(), "data")
	tests := []struct {
		name    string
		dataDir func(t *testing.T) string
		refused bool
	}{
		{"in this test's temp dir", func(t *testing.T) string { return filepath.Join(t.TempDir(), "data") }, false},
		{"in another test's temp dir", func(*testing.T) string { return other }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.dataDir(t)
			r := &fatalRecorder{TB: t}
			requireTestDataDir(r, &config.File{DataDir: dir, DBPath: filepath.Join(dir, "bridge.db"), MediaDir: filepath.Join(dir, "media")})
			switch {
			case !tc.refused && r.fatal != "":
				t.Errorf("refused a data dir inside the test's temp dir: %s", r.fatal)
			case tc.refused && !strings.Contains(r.fatal, "data_dir"):
				t.Errorf("Fatal(%q), want a refusal of data_dir %s", r.fatal, dir)
			}
		})
	}
}

// fatalRecorder is a testing.TB whose Fatal records the message instead of
// ending the test, so a test can check that a helper would have failed it.
type fatalRecorder struct {
	testing.TB
	fatal string
}

func (r *fatalRecorder) Fatal(args ...any) { r.fatal = fmt.Sprint(args...) }

func (r *fatalRecorder) Fatalf(format string, args ...any) { r.fatal = fmt.Sprintf(format, args...) }

// logContains reports whether a line captureLog collected contains substr.
func logContains(logs func() []string, substr string) bool {
	for _, l := range logs() {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

// brainFolder makes the folder at path a brain, scaffolded as Add brain does,
// and returns the brain_dirs entry that runs it with m approved. path goes
// into the config as given, and a ~ in it is the test's HOME, so set that
// first. A manifest without a name is named after its id.
func brainFolder(t testing.TB, path string, m manifest.Manifest) config.BrainRef {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if m.Name == "" {
		m.Name = m.ID
	}
	m = m.Normalized()
	if _, err := registry.Scaffold(fsx.ExpandHome(path, home), &m, brainTemplates); err != nil {
		t.Fatal(err)
	}
	return config.BrainRef{Path: path, Approved: &m}
}

// brainDirsJSON is the brain_dirs setting of a test config that runs refs,
// as text to append to its other settings: `, "brain_dirs": [...]`.
func brainDirsJSON(t testing.TB, refs ...config.BrainRef) string {
	t.Helper()
	data, err := json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	return `, "brain_dirs": ` + string(data)
}

// permOf returns the permission bits of path.
func permOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func TestMain(m *testing.M) {
	testutil.Main(m, testutil.MainOptions{
		Version:        claudecli.TestedClaudeVersion,
		RunMain:        main,
		BeforeChildren: func() { childEnvAllowlist = append(childEnvAllowlist, testChildEnv...) },
		BeforeTests:    func() { shutdownGrace = 0 },
		Ready: func(p testutil.Paths) {
			testHome, goToolHome, pkgDir, repoDir = p.Home, p.OriginalHome, p.PackageDir, p.RepoDir
		},
		Polls: func(w io.Writer) int {
			return pollsCmd(nil, toolIO{stdout: w, stderr: io.Discard, getenv: os.Getenv, now: time.Now()})
		},
		Extra: func() (bool, int) {
			if p := os.Getenv("FRIDOLIN_FAKE_CONNECT"); p != "" {
				return true, fakeConnect(p)
			}
			if os.Getenv("FRIDOLIN_FAKE_SESSION") == "1" {
				return true, fakeSession()
			}
			if os.Getenv("FRIDOLIN_FAKE_TTY") == "1" {
				return true, fakeTTY()
			}
			if p := os.Getenv("FRIDOLIN_FAKE_TTY_PARENT"); p != "" {
				return true, fakeTTYParent(p)
			}
			return false, 0
		},
	})
}
