package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBridgeScript stands in for the build setup.sh makes: it answers
// init, check, service status, service install and open from the FAKE_*
// variables, and notes every call in FAKE_LOG.
const fakeBridgeScript = `#!/bin/sh
echo "$*" >> "$FAKE_LOG"
case "$1 $2" in
  "init --config")
    mkdir -p "$(dirname "$3")" && echo '{}' > "$3" ;;
  "check --config")
    if [ -n "$FAKE_CHECK_FAIL" ]; then echo "loading config: $FAKE_CHECK_FAIL" >&2; exit 1; fi
    printf 'Config OK: %s\n  claude:    /opt/claude (2.1.400 (Claude Code))\n' "$3" ;;
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
// says.
const fakeGoScript = `#!/bin/sh
case "$1" in
  env) echo go1.99.0 ;;
  build)
    while [ $# -gt 0 ]; do [ "$1" = -o ] && out="$2"; shift; done
    cp "$FAKE_BRIDGE" "$out" ;;
  test)
    if [ -n "$FAKE_TESTS_FAIL" ]; then echo "--- FAIL: TestSomething"; exit 1; fi ;;
esac
`

func TestSetupSaysWhatHappened(t *testing.T) {
	// setup.sh used to offer a rollback after an install that changed
	// nothing, which would swap a working install for the one before, and
	// to tell someone with a bridge running to start a second one.
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	// Only what setup.sh runs: the real tools it needs, and stand-ins for
	// go, the compiler check and systemd. The bridge itself is the script
	// above, so nothing reaches launchctl, systemctl or a real bridge.
	bin := t.TempDir()
	for _, tool := range []string{"cat", "chmod", "cmp", "cp", "dirname", "id", "mkdir", "mktemp", "mv", "rm", "sed", "tail", "tr", "uname"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	scripts := map[string]string{"go": fakeGoScript, "xcode-select": "#!/bin/sh\n", "cc": "#!/bin/sh\n", "systemctl": "#!/bin/sh\n"}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// What transcription needs, on PATH only for the cases that name it.
	optional := t.TempDir()
	for _, name := range []string{"python3", "ffmpeg"} {
		if err := os.WriteFile(filepath.Join(optional, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	withOptional := "PATH=" + bin + string(os.PathListSeparator) + optional
	bridge := filepath.Join(t.TempDir(), "fridolin")
	if err := os.WriteFile(bridge, []byte(fakeBridgeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	// A copy of the checkout with no .git: the version is "dev" and no git
	// runs.
	repo := t.TempDir()
	for _, f := range []string{"setup.sh", "go.mod"} {
		data, err := os.ReadFile(filepath.Join(repoDir, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, f), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}

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
		{"python3 and ffmpeg are there", nil, []string{withOptional}, true, 0, true,
			[]string{"✓ python3 (for transcribing voice notes)", "✓ ffmpeg (for transcribing voice notes)"}, []string{"not installed — only needed to transcribe"}},
		// The test log gets a name of its own, not a fixed one in /tmp.
		{"the tests fail", nil, []string{"FAKE_TESTS_FAIL=1"}, true, 1, false,
			[]string{"--- FAIL: TestSomething", "Tests failed, so nothing was installed. Full output: $TMPDIR/fridolin-setup-tests."}, []string{"fridolin-setup-tests.log"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home, tmp := canonicalDir(t.TempDir()), canonicalDir(t.TempDir())
			calls := filepath.Join(t.TempDir(), "calls")
			if c.oldBuild {
				makeFiles(t, home, ".local/lib/fridolin/fridolin")
			}
			cmd := exec.Command(bash, append([]string{filepath.Join(repo, "setup.sh")}, c.args...)...)
			cmd.Env = append([]string{"HOME=" + home, "PATH=" + bin, "TMPDIR=" + tmp, "FAKE_LOG=" + calls, "FAKE_BRIDGE=" + bridge}, c.env...)
			out, err := cmd.CombinedOutput()
			exit := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exit = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if exit != c.exit {
				t.Errorf("setup.sh exited %d, want %d:\n%s", exit, c.exit, out)
			}
			expand := strings.NewReplacer("$HOME", home, "$TMPDIR", tmp).Replace
			for _, w := range c.want {
				if !strings.Contains(string(out), expand(w)) {
					t.Errorf("setup.sh does not say %q:\n%s", expand(w), out)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(string(out), w) {
					t.Errorf("setup.sh says %q:\n%s", w, out)
				}
			}
			logged, _ := os.ReadFile(calls)
			if got := strings.Contains(string(logged), "service install"); got != c.installs {
				t.Errorf("ran service install: %v, want %v; calls:\n%s", got, c.installs, logged)
			}
		})
	}
}
