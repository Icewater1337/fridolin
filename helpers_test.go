package main

import (
	"encoding/json"
	"errors"
	"fmt"
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
// WCB_FAKE_CLAUDE=1 it prints a stream-json result echoing its working
// directory and the new message, after WCB_FAKE_SLEEP_MS. With
// WCB_FAKE_READ it also reports whether it could read that file, and with
// WCB_FAKE_ENV the value of that environment variable. Asked for
// --version, it answers testedClaudeVersion. Tests point
// claude_path at os.Args[0], so no script or real CLI is needed.
//
// Before the tests run it unsets the variables the bridge reads: a shell
// that exports FRIDOLIN_CONFIG for a real bridge would otherwise point the
// tests at that bridge's config. It also gives them an empty HOME, see
// useEmptyHome, moves them out of the checkout, see runInEmptyDir, and
// passes testChildEnv on to the programs the bridge starts.
func TestMain(m *testing.M) {
	if os.Getenv("WCB_FAKE_PLAUD") == "1" && len(os.Args) > 1 && isPlaudCommand(os.Args[1]) {
		os.Exit(fakePlaud(os.Args[1:]))
	}
	if os.Getenv("WCB_FAKE_CLAUDE") == "1" {
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
// of childEnvAllowlist: the WCB_FAKE_* variables that steer the fakes, this
// binary playing the Claude or Plaud CLI among them, and GORACE, which
// keeps each run of this binary from waiting a second on exit under -race.
var testChildEnv = []childEnvVar{
	{"WCB_FAKE_*", anyChild},
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

func fakeClaude() {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		// What the dashboard and `fridolin check` ask for.
		fmt.Println(testedClaudeVersion + " (Claude Code)")
		return
	}
	prompt := ""
	for i, a := range args {
		if a == "-p" && i+1 < len(args) {
			prompt = args[i+1]
		}
	}
	if os.Getenv("WCB_FAKE_CHILD") == "1" {
		// A grandchild that inherits stdout, like a shell command Claude ran.
		child := exec.Command("sleep", "30")
		child.Stdout = os.Stdout
		_ = child.Start()
	}
	if ms, _ := strconv.Atoi(os.Getenv("WCB_FAKE_SLEEP_MS")); ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	if log := os.Getenv("WCB_FAKE_ARGS_LOG"); log != "" {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, strings.Join(args[:len(args)-1], " "))
			f.Close()
		}
	}
	if log := os.Getenv("WCB_FAKE_ENV_LOG"); log != "" {
		// What a brain inherits, one VAR=value per line.
		_ = os.WriteFile(log, []byte(strings.Join(os.Environ(), "\n")+"\n"), 0o600)
	}
	msg := prompt
	if i := strings.LastIndex(prompt, "<new_message>\n"); i >= 0 {
		msg = strings.TrimSuffix(strings.TrimSpace(prompt[i+len("<new_message>\n"):]), "</new_message>")
		msg = strings.TrimSpace(msg)
	}
	wd, _ := os.Getwd()
	out := fmt.Sprintf("echo:%s:%s", wd, msg)
	if p := os.Getenv("WCB_FAKE_READ"); p != "" {
		switch _, err := os.ReadFile(p); {
		case err == nil:
			out += " read=ok"
		case errors.Is(err, fs.ErrPermission):
			out += " read=denied"
		default:
			out += " read=error: " + err.Error()
		}
	}
	if name := os.Getenv("WCB_FAKE_ENV"); name != "" {
		if v, ok := os.LookupEnv(name); ok {
			out += " " + name + "=" + v
		} else {
			out += " " + name + " unset"
		}
	}
	q := strconv.Quote(out)
	fmt.Printf(`{"type":"assistant","message":{"content":[{"type":"text","text":%s}]}}`+"\n", q)
	fmt.Printf(`{"type":"result","result":%s}`+"\n", q)
}

// fakeClaudeEnv makes brains in this test spawn the fake CLI.
func fakeClaudeEnv(t *testing.T, sleepMS int) string {
	t.Helper()
	t.Setenv("WCB_FAKE_CLAUDE", "1")
	t.Setenv("WCB_FAKE_SLEEP_MS", strconv.Itoa(sleepMS))
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

// fakePlaud plays Plaud's CLI from fixture files in WCB_FAKE_PLAUD_DIR:
// files.txt, file_<id>.txt, transcript_<id>.txt, summary_<id>.txt, and
// "account" (the id `me` reports). Like @plaud-ai/cli 0.3.15 it prints its
// spinner line to stderr and exits 2 only when the login is rejected: an
// "expired" file makes every call do that, "offline" makes every call exit 3
// (Plaud unreachable), and fail_<command> makes that command exit 1. Each
// call is logged with HOME, and the last call's environment is in env.log.
func fakePlaud(args []string) int {
	dir := os.Getenv("WCB_FAKE_PLAUD_DIR")
	if f, err := os.OpenFile(dir+"/calls.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintf(f, "HOME=%s %s\n", os.Getenv("HOME"), strings.Join(args, " "))
		f.Close()
	}
	_ = os.WriteFile(dir+"/env.log", []byte(strings.Join(os.Environ(), "\n")+"\n"), 0o600)
	token := os.Getenv("HOME") + "/.plaud/tokens.json"
	exists := func(name string) bool { _, err := os.Stat(dir + "/" + name); return err == nil }
	read := func(name string) string { b, _ := os.ReadFile(dir + "/" + name); return string(b) }
	if args[0] != "login" && args[0] != "logout" {
		fmt.Fprintf(os.Stderr, "- Fetching %s...\n", args[0])
		switch {
		case exists("expired"):
			fmt.Fprintln(os.Stderr, "✗ [AUTH_FAILED] Token invalid or expired. Run `plaud login`.")
			return 2
		case exists("offline"):
			fmt.Fprintln(os.Stderr, "✗ [UNREACHABLE] Cannot reach Plaud servers. Check your network.")
			return 3
		case exists("fail_" + args[0]):
			fmt.Fprintf(os.Stderr, "✗ [FETCH_FAILED] Failed to fetch %s.\n", args[0])
			return 1
		}
	}
	switch args[0] {
	case "login":
		if _, err := os.Stat(dir + "/login_wait"); err == nil {
			// Like the real CLI without a browser: print the URL, wait for the approval.
			fmt.Println("Could not open browser. Open this URL manually: https://example.com/oauth?client=x")
			for i := 0; i < 200; i++ {
				if _, err := os.Stat(dir + "/login_go"); err == nil {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
		_ = os.MkdirAll(os.Getenv("HOME")+"/.plaud", 0o700)
		_ = os.WriteFile(token, []byte(`{"fake":true}`), 0o600)
		fmt.Println("Logged in")
	case "logout":
		_ = os.Remove(token)
	case "me":
		id := strings.TrimSpace(read("account"))
		if id == "" {
			id = "u_1"
		}
		fmt.Printf("\nUser Info:\n\n  id: %s\n  nickname: You\n  email: you@example.com\n\n", id)
	case "files":
		fmt.Print(read("files.txt"))
	case "file", "transcript", "summary":
		fmt.Print(read(args[0] + "_" + args[1] + ".txt"))
	}
	return 0
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
