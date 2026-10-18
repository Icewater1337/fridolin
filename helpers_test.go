package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
// FRIDOLIN_FAKE_PROMPT_LOG its prompt, and with
// FRIDOLIN_FAKE_STDERR_BYTES it floods stderr and fails instead. Asked for
// --version, it answers testedClaudeVersion. Tests point claude_path at
// os.Args[0], so no script or real CLI is needed. With
// FRIDOLIN_TEST_RUN_MAIN=1 it is the bridge itself instead, see
// runBridgeCLI.
//
// Before the tests run it unsets the variables the bridge reads: a shell
// that exports FRIDOLIN_CONFIG for a real bridge would otherwise point the
// tests at that bridge's config. It also gives them an empty HOME, see
// useEmptyHome, moves them out of the checkout, see runInEmptyDir, and
// passes testChildEnv on to the programs the bridge starts.
func TestMain(m *testing.M) {
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
	childEnvAllowlist = append(childEnvAllowlist, testChildEnv...)
	for _, k := range bridgeVariables() {
		if err := os.Unsetenv(k); err != nil {
			fmt.Fprintf(os.Stderr, "unsetting %s: %v\n", k, err)
			os.Exit(1)
		}
	}
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

// testHome is the HOME the tests start with.
var testHome string

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
