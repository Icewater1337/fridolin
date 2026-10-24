package app

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// fakeBridgeScript stands in for the build setup.sh makes: it answers
// paths, init, check, service status, service install and open from the
// FAKE_* variables, and notes every call in FAKE_LOG. paths names the
// --config given, else FRIDOLIN_CONFIG, else FAKE_CONFIG or the default,
// and FAKE_DATA_DIR or the default data dir.
const fakeBridgeScript = `#!/bin/sh
echo "$*" >> "$FAKE_LOG"
case "$1 $2" in
  "paths "|"paths --config")
    config="${3:-${FRIDOLIN_CONFIG:-${FAKE_CONFIG:-$HOME/.fridolin/config.json}}}"
    printf 'config=%s\nconfig_source=fake\ndata_dir=%s\ndata_dir_source=fake\n' "$config" "${FAKE_DATA_DIR:-$HOME/.fridolin}" ;;
  "init --config")
    mkdir -p "$(dirname "$3")" && echo '{}' > "$3" ;;
  "check --config")
    if [ -n "$FAKE_CHECK_FAIL" ]; then echo "loading config: $FAKE_CHECK_FAIL" >&2; exit 1; fi
    printf 'Config OK: %s\n  claude:    /opt/claude (2.1.400 (Claude Code))\n' "$3"
    if [ -n "$FAKE_CHECK_ISSUE" ]; then printf '  ! %s\n' "$FAKE_CHECK_ISSUE"; fi
    if [ -n "$FAKE_CHECK_LEFT_OUT" ]; then echo "brain personal cannot run; see the issue above" >&2; exit 3; fi ;;
  "service status")
    exit "${FAKE_STATUS:-3}" ;;
  "service install")
    dir="$6"
    case "$FAKE_INSTALL" in
      refuse) echo "fridolin service install: inline brains no longer load (nothing was changed)" >&2; exit 5 ;;
      fail-before) echo "fridolin service install: installing the binary: no space left" >&2; exit 1 ;;
    esac
    mkdir -p "$dir"
    if [ -e "$dir/fridolin" ]; then mv "$dir/fridolin" "$dir/fridolin.prev"; fi
    cp "$0" "$dir/fridolin"
    if [ "$FAKE_INSTALL" = fail-after ]; then echo "fridolin service install: the bridge did not answer" >&2; exit 1; fi ;;
esac
`

// fakeGoScript builds fakeBridgeScript and runs the tests as FAKE_TESTS_FAIL
// says. It notes its builds and test runs in FAKE_LOG, as "go ARGS".
const fakeGoScript = `#!/bin/sh
[ "$1" = env ] || echo "go $*" >> "$FAKE_LOG"
case "$1" in
  env) echo go1.99.0 ;;
  build)
    while [ $# -gt 0 ]; do [ "$1" = -o ] && out="$2"; shift; done
    cp "$FAKE_BRIDGE" "$out" ;;
  test)
    if [ -n "$FAKE_TESTS_FAIL" ]; then echo "--- FAIL: TestSomething"; exit 1; fi ;;
esac
`

// setupHarness runs a copy of setup.sh with only what it needs on PATH:
// the real tools, the real make, and stand-ins for go, the compiler check
// and systemd. The bridge itself is fakeBridgeScript, so nothing reaches
// launchctl, systemctl or a real bridge.
type setupHarness struct {
	bash, repo, bin, bridge string
	// optional holds what transcription needs, on PATH only for the cases
	// that add it.
	optional string
}

func newSetupHarness(t *testing.T) *setupHarness {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	makeBin, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is not installed")
	}
	h := &setupHarness{bash: bash, bin: t.TempDir(), optional: t.TempDir(), repo: t.TempDir()}
	for _, tool := range []string{"cat", "chmod", "cmp", "cp", "dirname", "id", "mkdir", "mktemp", "mv", "rm", "sed", "tail", "tr", "uname"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(p, filepath.Join(h.bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	scripts := map[string]string{
		"go":           fakeGoScript,
		"xcode-select": "#!/bin/sh\n",
		"cc":           "#!/bin/sh\n",
		"systemctl":    "#!/bin/sh\n",
		// The real make, with each call noted in FAKE_LOG.
		"make": "#!/bin/sh\necho \"make $*\" >> \"$FAKE_LOG\"\nexec '" + makeBin + "' \"$@\"\n",
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(h.bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"python3", "ffmpeg"} {
		if err := os.WriteFile(filepath.Join(h.optional, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.bridge = filepath.Join(t.TempDir(), "fridolin")
	if err := os.WriteFile(h.bridge, []byte(fakeBridgeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	// A copy of the checkout with no .git: the version is "dev" and no git
	// runs.
	for _, f := range []string{"setup.sh", "go.mod", "Makefile"} {
		data, err := os.ReadFile(filepath.Join(repoDir, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(h.repo, f), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// withOptional is the PATH entry that also puts python3 and ffmpeg on PATH.
func (h *setupHarness) withOptional() string {
	return "PATH=" + h.bin + string(os.PathListSeparator) + h.optional
}

// run runs setup.sh with args and HOME home, in an environment of nothing
// but the harness's variables and env, and returns its output, its exit
// status and the calls the stand-ins noted. The output names its TMPDIR
// as $TMPDIR.
func (h *setupHarness) run(t *testing.T, home string, args, env []string) (out string, exit int, calls string) {
	t.Helper()
	tmp := fsx.Canonical(t.TempDir())
	log := filepath.Join(t.TempDir(), "calls")
	cmd := exec.Command(h.bash, append([]string{filepath.Join(h.repo, "setup.sh")}, args...)...)
	cmd.Env = append([]string{"HOME=" + home, "PATH=" + h.bin, "TMPDIR=" + tmp, "FAKE_LOG=" + log, "FAKE_BRIDGE=" + h.bridge}, env...)
	b, err := cmd.CombinedOutput()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		exit = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	logged, _ := os.ReadFile(log)
	return strings.ReplaceAll(string(b), tmp, "$TMPDIR"), exit, string(logged)
}

func TestSetupSaysWhatHappened(t *testing.T) {
	t.Parallel()
	// setup.sh used to offer a rollback after an install that changed
	// nothing, which would swap a working install for the one before, and
	// to tell someone with a bridge running to start a second one.
	h := newSetupHarness(t)
	cases := []struct {
		name     string
		args     []string
		env      []string
		oldBuild bool // an earlier build is installed
		exit     int
		installs bool // setup.sh runs service install
		want     []string
		notWant  []string
	}{
		{"an upgrade", nil, nil, true, 0, true,
			[]string{"Setup complete", "The dashboard is open in your browser (or run: $HOME/.local/lib/fridolin/fridolin open)", "go back to the previous build"}, nil},
		{"a config this build does not load", nil, []string{"FAKE_CHECK_FAIL=inline brains no longer load"}, true, 1, false,
			[]string{"loading config: inline brains no longer load", "This build cannot load $HOME/.fridolin/config.json", "Nothing was installed"}, []string{"rollback"}},
		// check's issues used to be thrown away with the rest of its
		// report, and a brain it would leave out did not stop the install.
		{"a brain has drifted", nil, []string{"FAKE_CHECK_ISSUE=drift ~/notes: brain.json changed on disk"}, true, 0, true,
			[]string{"! drift ~/notes: brain.json changed on disk", "Setup complete"}, nil},
		{"a brain would be left out", nil, []string{"FAKE_CHECK_ISSUE=invalid ~/assistant/personal: the folder is ~/.ssh", "FAKE_CHECK_LEFT_OUT=1"}, true, 1, false,
			[]string{"! invalid ~/assistant/personal: the folder is ~/.ssh", "brain personal cannot run", "A brain in $HOME/.fridolin/config.json cannot run with this build", "Nothing was installed"}, []string{"This build cannot load", "rollback"}},
		{"the install is refused", nil, []string{"FAKE_INSTALL=refuse"}, true, 1, true,
			[]string{"(nothing was changed)", "The install was refused", "Nothing was installed"}, []string{"rollback"}},
		{"the install fails before the binary changes", nil, []string{"FAKE_INSTALL=fail-before"}, true, 1, true,
			[]string{"no space left", "The install or the restart failed"}, []string{"rollback", "Setup complete"}},
		{"the new build does not come up", nil, []string{"FAKE_INSTALL=fail-after"}, true, 1, true,
			[]string{"The new build is installed", "Go back to the build that ran before with: $HOME/.local/lib/fridolin/fridolin service rollback"}, []string{"Setup complete"}},
		{"the first install does not come up", nil, []string{"FAKE_INSTALL=fail-after"}, false, 1, true,
			[]string{"The install or the restart failed"}, []string{"rollback"}},
		// This build's open needs a bridge of its own version, which the
		// one left running may not be; the installed build's works.
		{"a bridge runs and the service is left alone", []string{"--no-service"}, []string{"FAKE_STATUS=0"}, true, 0, false,
			[]string{"the running bridge keeps its old build", "Open its dashboard: $HOME/.local/lib/fridolin/fridolin open"}, []string{"--open", "bin/fridolin open"}},
		{"a bridge runs, nothing is installed, and the service is left alone", []string{"--no-service"}, []string{"FAKE_STATUS=0"}, false, 0, false,
			[]string{"the running bridge keeps its old build", "bin/fridolin open (if the bridge is older than this"}, []string{"--open"}},
		{"no bridge runs and the service is left alone", []string{"--no-service"}, nil, false, 0, false,
			[]string{"Start the bridge:", "bin/fridolin --open"}, []string{"keeps its old build"}},
		// It cannot tell, so it does not say to start one regardless.
		{"http_listen is off and the service is left alone", []string{"--no-service"}, []string{"FAKE_STATUS=4"}, true, 0, false,
			[]string{"a bridge that is running keeps its old build", "If none is running, start it:", "bin/fridolin --open"}, []string{"Start the bridge:"}},
		// status cannot ask a bridge with http_listen off: that is not
		// "a bridge is running".
		{"http_listen is off", nil, []string{"FAKE_STATUS=4"}, true, 0, true,
			[]string{"If a bridge is running, installing restarts it"}, []string{"A bridge is running"}},
		{"a bridge is running", nil, []string{"FAKE_STATUS=0"}, true, 0, true,
			[]string{"A bridge is running. Installing restarts it"}, nil},
		// Transcription's needs are named, and missing ones do not stop it.
		{"python3 and ffmpeg are missing", nil, nil, true, 0, true,
			[]string{"python3 not installed — only needed to transcribe voice notes", "ffmpeg not installed — only needed to transcribe voice notes", "Setup complete"}, nil},
		{"python3 and ffmpeg are there", nil, []string{h.withOptional()}, true, 0, true,
			[]string{"✓ python3 (for transcribing voice notes)", "✓ ffmpeg (for transcribing voice notes)"}, []string{"not installed — only needed to transcribe"}},
		// The test log gets a name of its own, not a fixed one in /tmp.
		{"the tests fail", nil, []string{"FAKE_TESTS_FAIL=1"}, true, 1, false,
			[]string{"--- FAIL: TestSomething", "Tests failed, so nothing was installed. Full output: $TMPDIR/fridolin-setup-tests."}, []string{"fridolin-setup-tests.log"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel() // each run has its own HOME, TMPDIR and environment
			home := fsx.Canonical(t.TempDir())
			if c.oldBuild {
				makeFiles(t, home, ".local/lib/fridolin/fridolin")
			}
			out, exit, calls := h.run(t, home, c.args, c.env)
			if exit != c.exit {
				t.Errorf("setup.sh exited %d, want %d:\n%s", exit, c.exit, out)
			}
			expand := strings.NewReplacer("$HOME", home).Replace
			for _, w := range c.want {
				if !strings.Contains(out, expand(w)) {
					t.Errorf("setup.sh does not say %q:\n%s", expand(w), out)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(out, w) {
					t.Errorf("setup.sh says %q:\n%s", w, out)
				}
			}
			if got := strings.Contains(calls, "service install"); got != c.installs {
				t.Errorf("ran service install: %v, want %v; calls:\n%s", got, c.installs, calls)
			}
		})
	}
}

func TestSetupShowsTheFirstStepsOnlyToANewSetup(t *testing.T) {
	t.Parallel()
	// Every upgrade ended with "Pair WhatsApp", "Add a brain" and "Allow
	// your number", which a config already set up has done.
	h := newSetupHarness(t)
	firstSteps := []string{"Pair WhatsApp", "Add a brain", "Allow your number"}
	upgradeNote := `README.md, "Upgrading and rolling back"`
	for _, existing := range []bool{false, true} {
		home := fsx.Canonical(t.TempDir())
		makeFiles(t, home, ".local/lib/fridolin/fridolin")
		if existing {
			makeFiles(t, home, ".fridolin/config.json")
		}
		out, exit, _ := h.run(t, home, nil, nil)
		if exit != 0 {
			t.Fatalf("existing config %v: setup.sh exited %d:\n%s", existing, exit, out)
		}
		for _, w := range firstSteps {
			if got := strings.Contains(out, w); got == existing {
				t.Errorf("existing config %v: says %q: %v:\n%s", existing, w, got, out)
			}
		}
		if got := strings.Contains(out, upgradeNote); got != existing {
			t.Errorf("existing config %v: points to the upgrade notes: %v:\n%s", existing, got, out)
		}
	}
}

func TestSetupBuildsAndTestsWithTheMakefile(t *testing.T) {
	t.Parallel()
	// setup.sh had a go build and a go test of its own, which could drift
	// from the Makefile's that CI runs. It runs make build, stamped with
	// the version it names (this copy has no git, so "dev"), and make test.
	h := newSetupHarness(t)
	cases := []struct {
		name    string
		args    []string
		want    []string // calls, in order
		notWant []string
	}{
		{"with the tests", []string{"--no-service"}, []string{
			"make -s build VERSION=dev BINARY=",
			`go build -trimpath -ldflags -X github.com/Icewater1337/fridolin/internal/version.version=dev -o `,
			"make -s test",
			"go test -count=1 ./...",
		}, nil},
		{"--skip-tests", []string{"--no-service", "--skip-tests"}, []string{
			"make -s build VERSION=dev BINARY=",
			`go build -trimpath -ldflags -X github.com/Icewater1337/fridolin/internal/version.version=dev -o `,
		}, []string{"make -s test", "go test"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// An exported VERSION is some other software's: the build gets
			// the checkout's.
			out, exit, calls := h.run(t, fsx.Canonical(t.TempDir()), c.args, []string{"VERSION=9.9-not-ours"})
			if exit != 0 {
				t.Fatalf("setup.sh exited %d:\n%s", exit, out)
			}
			rest := calls
			for _, w := range c.want {
				i := strings.Index(rest, w)
				if i < 0 {
					t.Fatalf("no call %q after the ones before it; calls:\n%s", w, calls)
				}
				rest = rest[i+len(w):]
			}
			for _, w := range append(c.notWant, "9.9-not-ours") {
				if strings.Contains(calls, w) {
					t.Errorf("a call has %q; calls:\n%s", w, calls)
				}
			}
			if !strings.Contains(out, "built dev") {
				t.Errorf("setup.sh does not name the version it built:\n%s", out)
			}
		})
	}

	t.Run("make is missing", func(t *testing.T) {
		noMake := t.TempDir()
		entries, err := os.ReadDir(h.bin)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Name() != "make" {
				if err := os.Symlink(filepath.Join(h.bin, e.Name()), filepath.Join(noMake, e.Name())); err != nil {
					t.Fatal(err)
				}
			}
		}
		out, exit, calls := h.run(t, fsx.Canonical(t.TempDir()), []string{"--no-service"}, []string{"PATH=" + noMake})
		if exit != 1 || !strings.Contains(out, "make is not installed") {
			t.Errorf("setup.sh exited %d, want 1 and a word about make:\n%s", exit, out)
		}
		if strings.Contains(calls, "go build") {
			t.Errorf("setup.sh built without make; calls:\n%s", calls)
		}
	})
}

func TestSetupTakesItsFoldersFromFridolinVariables(t *testing.T) {
	t.Parallel()
	// setup.sh read DATA_DIR and INSTALL_DIR, names other software exports
	// too, and kept its own copy of the bridge's data-dir rules. It reads
	// FRIDOLIN_DATA_DIR and FRIDOLIN_INSTALL_DIR, and asks the build for
	// the config file and the data folder. An old name is ignored, but one
	// that names this install's config or binary stops it, as CONFIG_FILE
	// does: going on would set the service up with another config, or
	// install a second copy. $HOME stands for the home folder.
	h := newSetupHarness(t)
	const (
		defaultConfig  = "--config $HOME/.fridolin/config.json"
		defaultInstall = "--dir $HOME/.local/lib/fridolin "
	)
	cases := []struct {
		name  string
		env   []string
		files []string // made in the home folder first
		exit  int
		// calls lists parts of the calls setup.sh makes, in order;
		// notCalls must be in none of them.
		calls, notCalls []string
		want, notWant   []string // in the output
		dirs            []string // folders setup.sh must have made
	}{
		{"the defaults", nil, nil, 0,
			[]string{"paths\n", "init " + defaultConfig, "check " + defaultConfig, "service install " + defaultConfig + " " + defaultInstall},
			[]string{"--data-dir"}, []string{"wrote a starter config to $HOME/.fridolin/config.json"}, []string{"ignored"},
			[]string{".fridolin"}},
		{"FRIDOLIN_DATA_DIR for a new config", []string{"FRIDOLIN_DATA_DIR=$HOME/data"}, nil, 0,
			[]string{"paths --config $HOME/data/config.json\n", "init --config $HOME/data/config.json", "--data-dir $HOME/data", "service install --config $HOME/data/config.json " + defaultInstall},
			nil, nil, nil, []string{"data"}},
		{"FRIDOLIN_DATA_DIR with FRIDOLIN_CONFIG", []string{"FRIDOLIN_DATA_DIR=$HOME/data", "FRIDOLIN_CONFIG=$HOME/cfg/config.json"}, nil, 0,
			[]string{"paths\n", "init --config $HOME/cfg/config.json", "--data-dir $HOME/data", "service install --config $HOME/cfg/config.json"},
			nil, nil, nil, []string{"data"}},
		{"FRIDOLIN_DATA_DIR for a config that has one", []string{"FRIDOLIN_DATA_DIR=$HOME/data", "FAKE_DATA_DIR=$HOME/its-own"}, []string{"data/config.json"}, 0,
			[]string{"check --config $HOME/data/config.json", "service install --config $HOME/data/config.json"},
			[]string{"init "}, []string{"FRIDOLIN_DATA_DIR only goes into a new config: this one keeps its data folder, $HOME/its-own."}, nil,
			[]string{"its-own"}},
		{"the folders the build names", []string{"FAKE_CONFIG=$HOME/.whatsapp-claude-bridge/config.json", "FAKE_DATA_DIR=$HOME/.whatsapp-claude-bridge"}, nil, 0,
			[]string{"init --config $HOME/.whatsapp-claude-bridge/config.json", "service install --config $HOME/.whatsapp-claude-bridge/config.json"},
			[]string{"--data-dir"}, nil, nil, []string{".whatsapp-claude-bridge"}},
		{"FRIDOLIN_INSTALL_DIR", []string{"FRIDOLIN_INSTALL_DIR=$HOME/opt/fridolin"}, nil, 0,
			[]string{"service install " + defaultConfig + " --dir $HOME/opt/fridolin "},
			nil, []string{"$HOME/opt/fridolin/fridolin open"}, nil, nil},
		{"DATA_DIR is ignored", []string{"DATA_DIR=$HOME/other-software"}, nil, 0,
			[]string{"init " + defaultConfig, "service install " + defaultConfig},
			[]string{"--data-dir", "other-software"}, []string{"DATA_DIR is ignored; set FRIDOLIN_DATA_DIR"}, nil, nil},
		{"DATA_DIR naming the config in use", []string{"DATA_DIR=$HOME/.fridolin"}, []string{".fridolin/config.json"}, 0,
			[]string{"service install " + defaultConfig},
			nil, []string{"DATA_DIR is ignored"}, nil, nil},
		{"DATA_DIR holding another config", []string{"DATA_DIR=$HOME/data"}, []string{"data/config.json"}, 1,
			nil, []string{"init ", "check ", "service "},
			[]string{"DATA_DIR is no longer read, and $HOME/data holds a config.json", `FRIDOLIN_DATA_DIR="$HOME/data" $REPO/setup.sh --yes`, "env -u DATA_DIR $REPO/setup.sh --yes"}, nil, nil},
		{"DATA_DIR next to FRIDOLIN_DATA_DIR", []string{"DATA_DIR=$HOME/data", "FRIDOLIN_DATA_DIR=$HOME/new"}, []string{"data/config.json"}, 0,
			[]string{"service install --config $HOME/new/config.json"},
			nil, []string{"DATA_DIR is ignored"}, nil, []string{"new"}},
		{"INSTALL_DIR is ignored", []string{"INSTALL_DIR=$HOME/other-software"}, nil, 0,
			[]string{"service install " + defaultConfig + " " + defaultInstall},
			[]string{"other-software"}, []string{"INSTALL_DIR is ignored; set FRIDOLIN_INSTALL_DIR"}, nil, nil},
		{"INSTALL_DIR naming an install", []string{"INSTALL_DIR=$HOME/opt"}, []string{"opt/fridolin"}, 1,
			nil, []string{"paths", "service "},
			[]string{"INSTALL_DIR is no longer read, and Fridolin is installed in $HOME/opt", `FRIDOLIN_INSTALL_DIR="$HOME/opt" $REPO/setup.sh --yes`, "env -u INSTALL_DIR $REPO/setup.sh --yes"}, nil, nil},
		{"INSTALL_DIR next to FRIDOLIN_INSTALL_DIR", []string{"INSTALL_DIR=$HOME/opt", "FRIDOLIN_INSTALL_DIR=$HOME/new"}, []string{"opt/fridolin"}, 0,
			[]string{"service install " + defaultConfig + " --dir $HOME/new "},
			nil, []string{"INSTALL_DIR is ignored"}, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel() // each run has its own HOME, TMPDIR and environment
			home := fsx.Canonical(t.TempDir())
			expand := strings.NewReplacer("$HOME", home, "$REPO", h.repo).Replace
			if len(c.files) > 0 {
				makeFiles(t, home, c.files...)
			}
			env := []string{}
			for _, e := range c.env {
				env = append(env, expand(e))
			}
			out, exit, calls := h.run(t, home, []string{"--yes"}, env)
			if exit != c.exit {
				t.Fatalf("setup.sh exited %d, want %d:\n%s", exit, c.exit, out)
			}
			rest := calls
			for _, w := range c.calls {
				i := strings.Index(rest, expand(w))
				if i < 0 {
					t.Fatalf("no call with %q after the ones before it; calls:\n%s", expand(w), calls)
				}
				rest = rest[i+len(expand(w)):]
			}
			for _, w := range c.notCalls {
				if strings.Contains(calls, expand(w)) {
					t.Errorf("a call has %q; calls:\n%s", expand(w), calls)
				}
			}
			for _, w := range c.want {
				if !strings.Contains(out, expand(w)) {
					t.Errorf("setup.sh does not say %q:\n%s", expand(w), out)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(out, expand(w)) {
					t.Errorf("setup.sh says %q:\n%s", expand(w), out)
				}
			}
			for _, d := range c.dirs {
				st, err := os.Stat(filepath.Join(home, d))
				if err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
					t.Errorf("%s: %v, want a folder only you can open", d, err)
				}
			}
		})
	}
}
