package testutil

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestMain lets the test binary stand in for the Claude CLI: with
// FRIDOLIN_FAKE_CLAUDE=1 it prints a stream-json result echoing its working
// directory and the new message, after FRIDOLIN_FAKE_SLEEP_MS and after
// FRIDOLIN_FAKE_WAIT_FILE exists when set. With
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
// with FRIDOLIN_FAKE_STDERR_BYTES it floods stderr and fails instead. With
// FRIDOLIN_FAKE_WAIT_BRAIN only that brain's runs wait for
// FRIDOLIN_FAKE_WAIT_FILE, and with FRIDOLIN_FAKE_MCP a message holding
// #mcp plays those steps against the run's fridolin MCP server (fakeMCP).
// Asked for
// --version, it answers testedClaudeVersion, or FRIDOLIN_FAKE_VERSION as
// it is when that is set. Tests point claude_path at
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
func Main(m *testing.M, options ...MainOptions) {
	var opts MainOptions
	if len(options) > 0 {
		opts = options[0]
	}
	if reexecutedInPlace() {
		fmt.Fprintln(os.Stderr, "TestMain: a restart re-executed the bridge, and with it this test binary in place (serve in a test, with the real execSelf?): the tests that were running never finished")
		os.Exit(2)
	}
	if os.Getenv("FRIDOLIN_TEST_RUN_MAIN") == "1" && opts.RunMain != nil {
		// Not FRIDOLIN_FAKE_*, so the programs this bridge starts do not
		// get it, and the fake CLIs among them stay fakes.
		if opts.BeforeChildren != nil {
			opts.BeforeChildren()
		}
		opts.RunMain()
		os.Exit(0)
	}
	if os.Getenv("FRIDOLIN_FAKE_PLAUD") == "1" && len(os.Args) > 1 && isPlaudCommand(os.Args[1]) {
		os.Exit(fakePlaud(os.Args[1:]))
	}
	if raw := os.Getenv(scenarioEnv); raw != "" {
		os.Exit(scenarioMain(raw))
	}
	if os.Getenv("FRIDOLIN_FAKE_CLAUDE") == "1" {
		runFakeClaude(opts.Version, opts.Polls)
		return
	}
	if opts.Extra != nil {
		if handled, code := opts.Extra(); handled {
			os.Exit(code)
		}
	}
	if opts.BeforeChildren != nil {
		opts.BeforeChildren()
	}

	if opts.BeforeTests != nil {
		opts.BeforeTests()
	}
	paths, cleanup, err := Isolate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sharedMainInstalled = true
	if opts.Ready != nil {
		opts.Ready(paths)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)

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
	sharedPaths.Home = dir
	return func() { os.RemoveAll(dir) }, nil
}

// runInEmptyDir runs the tests in a new, empty working directory. A relative
// path in a test must not reach the checkout, where a developer may keep a
// config.json. Only the package's testdata is linked in, see linkTestdata.
func isolateWorkingDir() (func(), error) {
	var err error
	if sharedPaths.PackageDir, err = os.Getwd(); err != nil {
		return nil, err
	}
	// A test that runs this binary again starts it in the empty directory
	// it runs in itself, whose testdata link leads back to the package.
	if target, err := os.Readlink(filepath.Join(sharedPaths.PackageDir, "testdata")); err == nil && filepath.IsAbs(target) {
		sharedPaths.PackageDir = filepath.Dir(target)
	}
	if sharedPaths.RepoDir, err = moduleRoot(sharedPaths.PackageDir); err != nil {
		return nil, err
	}
	// Tests run this binary again through os.Args[0], which must keep
	// working from the new directory when it was started by a relative path.
	if strings.ContainsRune(os.Args[0], filepath.Separator) {
		if os.Args[0], err = filepath.Abs(os.Args[0]); err != nil {
			return nil, err
		}
	}
	wd, err := os.MkdirTemp("", "fridolin-test-wd-")
	if err != nil {
		return nil, err
	}
	ready := false
	defer func() {
		if !ready {
			os.RemoveAll(wd)
		}
	}()
	if err := os.Chdir(wd); err != nil {
		return nil, err
	}
	if err := os.Setenv("PWD", wd); err != nil {
		return nil, err
	}
	if err := LinkTestdata(sharedPaths.PackageDir, wd); err != nil {
		return nil, err
	}
	ready = true
	return func() { os.RemoveAll(wd) }, nil
}

// moduleRoot is the folder of the go.mod at or above dir.
func moduleRoot(dir string) (string, error) {
	for root := dir; ; root = filepath.Dir(root) {
		if st, err := os.Stat(filepath.Join(root, "go.mod")); err == nil && st.Mode().IsRegular() {
			return root, nil
		}
		if filepath.Dir(root) == root {
			return "", fmt.Errorf("no go.mod at or above %s", dir)
		}
	}
}

// linkTestdata makes testdata in the working directory wd the package's own.
// The fuzz engine reads a target's corpus from testdata/fuzz/<target> and
// saves a failing input there, both relative to the working directory: in
// the empty one, `go test` would skip the committed inputs, and `go test
// -fuzz` would save a crash into a directory that is removed when the tests
// end. The link is made even when the package has no testdata yet, so that
// saving a crash then fails loudly instead of going nowhere.
func LinkTestdata(pkgDir, wd string) error {
	return os.Symlink(filepath.Join(pkgDir, "testdata"), filepath.Join(wd, "testdata"))
}

// bridgeVariables are the environment variables the bridge reads:
// FRIDOLIN_CONFIG picks the config, and a false BRAIN_MODE refuses it.
func BridgeVariables() []string {
	return []string{"FRIDOLIN_CONFIG", "BRAIN_MODE"}
}

// sharedMainOptions connects command-specific subprocess modes to the shared
// fake process and isolated test harness. All callbacks run before m.Run.
type MainOptions struct {
	Version                              string
	RunMain, BeforeChildren, BeforeTests func()
	Extra                                func() (handled bool, exitCode int)
	Polls                                func(io.Writer) int
	Ready                                func(Paths)
}
type Paths struct{ Home, OriginalHome, PackageDir, RepoDir string }

var sharedPaths Paths
var sharedMainInstalled bool

// Isolate installs an empty HOME and working directory, clears bridge config
// selectors, and links the package's testdata. Call it before starting tests,
// never concurrently with them. The returned cleanup restores process state
// and removes only directories created by this call.
func Isolate() (Paths, func(), error) {
	originalDir, err := os.Getwd()
	if err != nil {
		return Paths{}, nil, fmt.Errorf("moving the tests out of the checkout: %w", err)
	}
	type entry struct {
		value string
		set   bool
	}
	saved := map[string]entry{}
	for _, k := range append(BridgeVariables(), "HOME", "PWD", testPIDVariable) {
		v, ok := os.LookupEnv(k)
		saved[k] = entry{v, ok}
	}
	restore := func() {
		_ = os.Chdir(originalDir)
		for k, e := range saved {
			if e.set {
				_ = os.Setenv(k, e.value)
			} else {
				_ = os.Unsetenv(k)
			}
		}
	}
	for _, k := range BridgeVariables() {
		if err := os.Unsetenv(k); err != nil {
			restore()
			return Paths{}, nil, fmt.Errorf("unsetting %s: %w", k, err)
		}
	}
	if err := os.Setenv(testPIDVariable, strconv.Itoa(os.Getpid())); err != nil {
		restore()
		return Paths{}, nil, fmt.Errorf("setting %s: %w", testPIDVariable, err)
	}
	sharedPaths.OriginalHome = os.Getenv("HOME")
	removeHome, err := useEmptyHome()
	if err != nil {
		restore()
		return Paths{}, nil, fmt.Errorf("giving the tests an empty HOME: %w", err)
	}
	removeDir, err := isolateWorkingDir()
	if err != nil {
		restore()
		removeHome()
		return Paths{}, nil, fmt.Errorf("moving the tests out of the checkout: %w", err)
	}
	return sharedPaths, func() { restore(); removeDir(); removeHome() }, nil
}
