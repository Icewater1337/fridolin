package main

import (
	"bufio"
	"bytes"
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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// TestMain lets the test binary stand in for the Claude CLI: with
// FRIDOLIN_FAKE_CLAUDE=1 it prints a stream-json result echoing its working
// directory and the new message, after FRIDOLIN_FAKE_SLEEP_MS. With
// FRIDOLIN_FAKE_READ it also reports whether it could read that file, with
// FRIDOLIN_FAKE_ENV the value of that environment variable, and with
// FRIDOLIN_FAKE_POLLS=1 what `fridolin polls` answers in the run. With
// FRIDOLIN_FAKE_ASK_ON a message ending in that asks a question first (and
// with FRIDOLIN_FAKE_HANG_AFTER_ASK=1 never answers, with
// FRIDOLIN_FAKE_SLEEP_AFTER_ASK_MS answers that much later), with
// FRIDOLIN_FAKE_ASK every run first calls AskUserQuestion with that input,
// FRIDOLIN_FAKE_PID_LOG gets each run's process ID and
// FRIDOLIN_FAKE_PROMPT_LOG its prompt, with FRIDOLIN_FAKE_SETSID_CHILD
// it leaves a grandchild in a session of its own holding its output, and
// with FRIDOLIN_FAKE_STDERR_BYTES it floods stderr and fails instead. Asked for
// --version, it answers testedClaudeVersion. Tests point claude_path at
// os.Args[0], so no script or real CLI is needed. With
// FRIDOLIN_FAKE_CONNECT it connects to that unix socket instead (see
// fakeConnect), with FRIDOLIN_FAKE_SESSION it reports its session (see
// fakeSession), with FRIDOLIN_FAKE_TTY or FRIDOLIN_FAKE_TTY_PARENT it
// plays a run that tries the terminal, or the bridge that starts it (see
// fakeTTY), and with FRIDOLIN_TEST_RUN_MAIN=1 it is the bridge itself,
// see runBridgeCLI.
//
// Before the tests run it unsets the variables the bridge reads: a shell
// that exports FRIDOLIN_CONFIG for a real bridge would otherwise point the
// tests at that bridge's config. It also gives them an empty HOME, see
// useEmptyHome, moves them out of the checkout, see runInEmptyDir, passes
// testChildEnv on to the programs the bridge starts, and has the bridges
// they start stop without a grace for their runs (shutdownGrace).
//
// A test binary that finds its own process ID in testPIDVariable has been
// executed again in place, and fails at once, see reexecutedInPlace.
func TestMain(m *testing.M) {
	if reexecutedInPlace() {
		fmt.Fprintln(os.Stderr, "TestMain: a restart re-executed the bridge, and with it this test binary in place (serve in a test, with the real execSelf?): the tests that were running never finished")
		os.Exit(2)
	}
	if os.Getenv("FRIDOLIN_TEST_RUN_MAIN") == "1" {
		// Not FRIDOLIN_FAKE_*, so the programs this bridge starts do not
		// get it, and the fake CLIs among them stay fakes.
		childEnvAllowlist = append(childEnvAllowlist, testChildEnv...)
		main()
		os.Exit(0)
	}
	if os.Getenv("FRIDOLIN_FAKE_PLAUD") == "1" && len(os.Args) > 1 && isPlaudCommand(os.Args[1]) {
		os.Exit(fakePlaud(os.Args[1:]))
	}
	if os.Getenv("FRIDOLIN_FAKE_CLAUDE") == "1" {
		fakeClaude()
		return
	}
	if p := os.Getenv("FRIDOLIN_FAKE_CONNECT"); p != "" {
		os.Exit(fakeConnect(p))
	}
	if os.Getenv("FRIDOLIN_FAKE_SESSION") == "1" {
		os.Exit(fakeSession())
	}
	if os.Getenv("FRIDOLIN_FAKE_TTY") == "1" {
		os.Exit(fakeTTY())
	}
	if spec := os.Getenv("FRIDOLIN_FAKE_TTY_PARENT"); spec != "" {
		os.Exit(fakeTTYParent(spec))
	}
	childEnvAllowlist = append(childEnvAllowlist, testChildEnv...)
	for _, k := range bridgeVariables() {
		if err := os.Unsetenv(k); err != nil {
			fmt.Fprintf(os.Stderr, "unsetting %s: %v\n", k, err)
			os.Exit(1)
		}
	}
	if err := os.Setenv(testPIDVariable, strconv.Itoa(os.Getpid())); err != nil {
		fmt.Fprintf(os.Stderr, "setting %s: %v\n", testPIDVariable, err)
		os.Exit(1)
	}
	goToolHome = os.Getenv("HOME")
	// The tests of the stop set a grace of their own.
	shutdownGrace = 0
	removeHome, err := useEmptyHome()
	if err != nil {
		fmt.Fprintf(os.Stderr, "giving the tests an empty HOME: %v\n", err)
		os.Exit(1)
	}
	code, err := runInEmptyDir(m)
	removeHome()
	if err != nil {
		fmt.Fprintf(os.Stderr, "moving the tests out of the checkout: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

// testChildEnv is what the programs the bridge starts get in tests on top
// of childEnvAllowlist: the FRIDOLIN_FAKE_* variables that steer the fakes, this
// binary playing the Claude or Plaud CLI among them, and GORACE, which
// keeps each run of this binary from waiting a second on exit under -race.
var testChildEnv = []childEnvVar{
	{"FRIDOLIN_FAKE_*", anyChild},
	{"GORACE", anyChild},
}

// testPIDVariable holds the process ID of the test binary that runs the
// tests. The programs it starts have their own, so only a process that is
// that binary executed again in place finds its own ID there.
const testPIDVariable = "FRIDOLIN_TEST_PID"

// reexecutedInPlace reports whether this process is the test binary
// executed again in place. exec keeps the process ID: a restart (serve)
// re-executes os.Args[0], which in a test is this binary, with the test
// flags and the environment of the test that was running. Playing the
// fake CLI, as that environment says, or running the tests again, it would
// exit 0, and go test would report the package as passing although the
// tests after that one never ran.
func reexecutedInPlace() bool {
	return os.Getenv(testPIDVariable) == strconv.Itoa(os.Getpid())
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
		tb.restart = func() { cancel(errRestart) }
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

// useEmptyHome points HOME at a new, empty temp directory and returns a
// function that removes it. Tests set their own HOME with t.Setenv; one that
// forgets and loads a config would otherwise resolve ~ and the default data
// dir, ~/.fridolin, in the real home, and loading the config creates the
// data dir and media/ there before any guard runs.
func useEmptyHome() (func(), error) {
	dir, err := os.MkdirTemp("", "fridolin-test-home-")
	if err != nil {
		return nil, err
	}
	if err := os.Setenv("HOME", dir); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	testHome = dir
	return func() { os.RemoveAll(dir) }, nil
}

// repoDir is the package directory the tests were started in. They run
// elsewhere, so a test that reads a file of the checkout joins it to this.
var repoDir string

// runInEmptyDir runs the tests in a new, empty working directory. A relative
// path in a test must not reach the checkout, where a developer may keep a
// config.json. Only the package's testdata is linked in, see linkTestdata.
func runInEmptyDir(m *testing.M) (int, error) {
	var err error
	if repoDir, err = os.Getwd(); err != nil {
		return 0, err
	}
	// Tests run this binary again through os.Args[0], which must keep
	// working from the new directory when it was started by a relative path.
	if strings.ContainsRune(os.Args[0], filepath.Separator) {
		if os.Args[0], err = filepath.Abs(os.Args[0]); err != nil {
			return 0, err
		}
	}
	wd, err := os.MkdirTemp("", "fridolin-test-wd-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(wd)
	if err := os.Chdir(wd); err != nil {
		return 0, err
	}
	if err := os.Setenv("PWD", wd); err != nil {
		return 0, err
	}
	if err := linkTestdata(repoDir, wd); err != nil {
		return 0, err
	}
	return m.Run(), nil
}

// linkTestdata makes testdata in the working directory wd the package's own.
// The fuzz engine reads a target's corpus from testdata/fuzz/<target> and
// saves a failing input there, both relative to the working directory: in
// the empty one, `go test` would skip the committed inputs, and `go test
// -fuzz` would save a crash into a directory that is removed when the tests
// end. The link is made even when the package has no testdata yet, so that
// saving a crash then fails loudly instead of going nowhere.
func linkTestdata(pkgDir, wd string) error {
	return os.Symlink(filepath.Join(pkgDir, "testdata"), filepath.Join(wd, "testdata"))
}

// bridgeVariables are the environment variables the bridge reads:
// FRIDOLIN_CONFIG picks the config, and a false BRAIN_MODE refuses it.
func bridgeVariables() []string {
	return []string{"FRIDOLIN_CONFIG", "BRAIN_MODE"}
}

// fakeNewMessage is the new message in prompt, as the fake CLI echoes it:
// "<from> says: <text>".
func fakeNewMessage(prompt string) string {
	msg := prompt
	if i := strings.LastIndex(prompt, "<new_message>\n"); i >= 0 {
		msg = strings.TrimSuffix(strings.TrimSpace(prompt[i+len("<new_message>\n"):]), "</new_message>")
		msg = strings.TrimSpace(msg)
	}
	var m promptMessage
	if err := json.Unmarshal([]byte(msg), &m); err == nil {
		return m.From + " says: " + m.Text
	}
	return msg
}

func fakeClaude() {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		// What the dashboard and `fridolin check` ask for.
		fmt.Println(testedClaudeVersion + " (Claude Code)")
		return
	}
	// Like the CLI, it reads the prompt from stdin when -p comes last.
	prompt := ""
	for i, a := range args {
		if a == "-p" && i+1 < len(args) {
			prompt = args[i+1]
		}
	}
	if len(args) > 0 && args[len(args)-1] == "-p" {
		data, _ := io.ReadAll(os.Stdin)
		prompt = string(data)
	}
	if log := os.Getenv("FRIDOLIN_FAKE_PID_LOG"); log != "" {
		// One line a run, so that a test can look for the process later.
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, os.Getpid())
			f.Close()
		}
	}
	if log := os.Getenv("FRIDOLIN_FAKE_PROMPT_LOG"); log != "" {
		// Each run's prompt after a line of its own (fakePromptsIn).
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%s\n%s\n", fakePromptMark, prompt)
			f.Close()
		}
	}
	if os.Getenv("FRIDOLIN_FAKE_CHILD") == "1" {
		// A grandchild that inherits stdout, like a shell command Claude ran.
		child := exec.Command("sleep", "30")
		child.Stdout = os.Stdout
		_ = child.Start()
	}
	if holds := os.Getenv("FRIDOLIN_FAKE_SETSID_CHILD"); holds != "" {
		// A grandchild that leaves the run's process group, as a daemon or
		// setsid does, holding stdout, or stdout and stderr with
		// "stdout,stderr": no kill of the group reaches it. Its PID goes
		// to FRIDOLIN_FAKE_PID_LOG after the CLI's own.
		child := exec.Command("sleep", "30")
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		child.Stdout = os.Stdout
		if strings.Contains(holds, "stderr") {
			child.Stderr = os.Stderr
		}
		if child.Start() == nil {
			if log := os.Getenv("FRIDOLIN_FAKE_PID_LOG"); log != "" {
				if f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
					fmt.Fprintln(f, child.Process.Pid)
					f.Close()
				}
			}
		}
	}
	if ms, _ := strconv.Atoi(os.Getenv("FRIDOLIN_FAKE_SLEEP_MS")); ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	if n, _ := strconv.Atoi(os.Getenv("FRIDOLIN_FAKE_STDERR_BYTES")); n > 0 {
		// A CLI that floods stderr and fails without a result: n bytes of
		// filler, then the line that says what went wrong.
		w := bufio.NewWriter(os.Stderr)
		for i := 0; i < n; i++ {
			_ = w.WriteByte('x')
		}
		fmt.Fprintln(w, "\nfatal: the last thing the CLI said")
		_ = w.Flush()
		os.Exit(1)
	}
	if log := os.Getenv("FRIDOLIN_FAKE_ARGS_LOG"); log != "" {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, strings.Join(args, " "))
			f.Close()
		}
	}
	if log := os.Getenv("FRIDOLIN_FAKE_RUN_LOG"); log != "" {
		// Each run as one JSON line: the arguments, with a prompt that
		// follows -p set apart, the prompt, from there or from stdin, and
		// the working directory.
		wd, _ := os.Getwd()
		argv := slices.Clone(args)
		if i := slices.Index(argv, "-p"); i >= 0 && i+1 < len(argv) {
			argv[i+1] = "<prompt>"
		}
		line, _ := json.Marshal(map[string]any{"args": argv, "prompt": prompt, "dir": wd})
		if f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "%s\n", line)
			f.Close()
		}
	}
	if log := os.Getenv("FRIDOLIN_FAKE_ENV_LOG"); log != "" {
		// What a brain inherits, one VAR=value per line.
		_ = os.WriteFile(log, []byte(strings.Join(os.Environ(), "\n")+"\n"), 0o600)
	}
	msg := fakeNewMessage(prompt)
	wd, _ := os.Getwd()
	out := fmt.Sprintf("echo:%s:%s", wd, msg)
	if p := os.Getenv("FRIDOLIN_FAKE_READ"); p != "" {
		switch _, err := os.ReadFile(p); {
		case err == nil:
			out += " read=ok"
		case errors.Is(err, fs.ErrPermission):
			out += " read=denied"
		default:
			out += " read=error: " + err.Error()
		}
	}
	if os.Getenv("FRIDOLIN_FAKE_POLLS") == "1" {
		// A tool the run starts, asking the bridge with the run's token.
		var res bytes.Buffer
		code := pollsCmd(nil, toolIO{stdout: &res, stderr: io.Discard, getenv: os.Getenv, now: time.Now()})
		out += fmt.Sprintf(" polls=%d %s", code, strings.Join(strings.Fields(res.String()), " "))
	}
	if name := os.Getenv("FRIDOLIN_FAKE_ENV"); name != "" {
		if v, ok := os.LookupEnv(name); ok {
			out += " " + name + "=" + v
		} else {
			out += " " + name + " unset"
		}
	}
	if ask := os.Getenv("FRIDOLIN_FAKE_ASK_ON"); ask != "" && strings.HasSuffix(msg, ask) {
		// A message ending in it asks the chat a question first.
		fmt.Println(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{"questions":[{"question":"Which day suits you?"}]}}]}}`)
		if os.Getenv("FRIDOLIN_FAKE_HANG_AFTER_ASK") == "1" {
			// A run that hangs once it has asked, until it is stopped.
			time.Sleep(30 * time.Second)
			return
		}
		if ms, _ := strconv.Atoi(os.Getenv("FRIDOLIN_FAKE_SLEEP_AFTER_ASK_MS")); ms > 0 {
			// A run that is still at work when the answer comes.
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
	}
	if ask := os.Getenv("FRIDOLIN_FAKE_ASK"); ask != "" {
		// An AskUserQuestion call, its input as given, which nobody answers.
		fmt.Printf(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":%s}]}}`+"\n", ask)
	}
	if result := os.Getenv("FRIDOLIN_FAKE_RESULT"); result != "" {
		fmt.Println(result)
		return
	}
	q := strconv.Quote(out)
	fmt.Printf(`{"type":"assistant","message":{"content":[{"type":"text","text":%s}]}}`+"\n", q)
	fmt.Printf(`{"type":"result","result":%s}`+"\n", q)
}

// loggedPrompts returns the prompts the fake CLI logged to path, by the
// text of their new message.
func loggedPrompts(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range fakePromptsIn(t, path) {
		p = strings.TrimSuffix(p, "\n")
		out[fakeNewMessage(p)] = p
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
// for isolateProcessGroup, as a brain's run gets it) and prints its output.
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
		isolateProcessGroup(cmd)
	}
	out, err := cmd.CombinedOutput()
	os.Stdout.Write(out)
	if err != nil {
		fmt.Println("child:", err)
		return 1
	}
	return 0
}

// fakeClaudeEnv makes brains in this test spawn the fake CLI.
// fakePromptMark starts each prompt in FRIDOLIN_FAKE_PROMPT_LOG.
const fakePromptMark = "=====PROMPT====="

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
	return strings.Split(string(data), fakePromptMark+"\n")[1:]
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
func requireTestDataDir(t testing.TB, cfg *Config) {
	t.Helper()
	// t.TempDir hands out numbered folders inside one directory per test.
	if err := checkTestDataDir(filepath.Dir(t.TempDir()), cfg); err != nil {
		t.Fatal(err)
	}
}

// checkTestDataDir reports the first data path of cfg outside root. Paths
// may use root as given or with its symlinks resolved (/var and /private/var
// on macOS).
func checkTestDataDir(root string, cfg *Config) error {
	roots := []string{filepath.Clean(root), canonicalDir(root)}
	for _, p := range []struct{ field, path string }{
		{"data_dir", cfg.DataDir},
		{"db_path", cfg.DBPath},
		{"media_dir", cfg.MediaDir},
	} {
		inside := false
		for _, r := range roots {
			inside = inside || isWithin(p.path, r)
		}
		if !inside {
			return fmt.Errorf("%s %q is outside the test's temp dir %s: refusing to touch it", p.field, p.path, root)
		}
	}
	return nil
}

// clearEnv unsets keys for the rest of the test and restores them afterwards,
// so a value the code under test exports cannot leak into the next test.
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "") // registers the restore
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

// waitFor polls cond until it holds or the timeout passes.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func isPlaudCommand(c string) bool {
	switch c {
	case "files", "file", "transcript", "summary", "me", "login", "logout":
		return true
	}
	return false
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
		{"inside through the resolved root", canonicalDir(root) + "/data", canonicalDir(root) + "/data/bridge.db", canonicalDir(root) + "/data/media", ""},
		{"data dir outside", out, in + "/bridge.db", in + "/media", "data_dir"},
		{"database outside", in, out + "/bridge.db", in + "/media", "db_path"},
		{"media outside", in, in + "/bridge.db", out + "/media", "media_dir"},
		{"sibling sharing the prefix", root + "x/data", in + "/bridge.db", in + "/media", "data_dir"},
		{"relative to the working directory", "data", in + "/bridge.db", in + "/media", "data_dir"},
		{"empty", "", in + "/bridge.db", in + "/media", "data_dir"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkTestDataDir(root, &Config{DataDir: tc.data, DBPath: tc.db, MediaDir: tc.media})
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
			requireTestDataDir(r, &Config{DataDir: dir, DBPath: filepath.Join(dir, "bridge.db"), MediaDir: filepath.Join(dir, "media")})
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
func brainFolder(t testing.TB, path string, m BrainManifest) BrainRef {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if m.Name == "" {
		m.Name = m.ID
	}
	m = m.normalized()
	if _, err := ScaffoldBrain(expandHome(path, home), &m); err != nil {
		t.Fatal(err)
	}
	return BrainRef{Path: path, Approved: &m}
}

// brainDirsJSON is the brain_dirs setting of a test config that runs refs,
// as text to append to its other settings: `, "brain_dirs": [...]`.
func brainDirsJSON(t testing.TB, refs ...BrainRef) string {
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
