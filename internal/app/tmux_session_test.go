package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// TestViewerGetsTheAllowlistedEnvironment: a tmux server the viewer starts
// keeps the environment it was started with, and any brain can read that
// from it. So every tmux call, and the osascript that opens the Terminal
// window, gets what childEnv gives any program and the names in
// child_env_passthrough, not the bridge's whole environment.
func TestViewerGetsTheAllowlistedEnvironment(t *testing.T) {
	// Serial: replaces tmuxPath, osascript and startViewer, package
	// variables, and sets TMUX_TMPDIR in the process's environment.
	logs := t.TempDir()
	bin := t.TempDir()
	// Each call records its environment under its first argument.
	fake := "#!/bin/sh\nenv > \"" + logs + "/$(basename \"$0\").$1\"\n"
	for _, name := range []string{"tmux", "osascript"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(fake), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Use up findTmux's lookup, so that it returns the fake, and start a
	// fresh one afterwards, so that a later test finds the real tmux again.
	tmuxOnce.Do(func() {})
	oldTmux, oldOsascript := tmuxPath, osascript
	tmuxPath, osascript = filepath.Join(bin, "tmux"), filepath.Join(bin, "osascript")
	t.Cleanup(func() { tmuxPath, osascript, tmuxOnce = oldTmux, oldOsascript, sync.Once{} })
	prev := startViewer
	startViewer = NewTmuxViewer
	t.Cleanup(func() { startViewer = prev })
	for k, v := range map[string]string{
		"TMUX_TMPDIR":             filepath.Join(logs, "tmux"),
		"GROQ_API_KEY":            "test-groq-key",
		"CLAUDE_CODE_OAUTH_TOKEN": "test-oauth-token",
		"FRIDOLIN_TEST_SECRET":    "test-secret",
	} {
		t.Setenv(k, v)
	}

	dir := t.TempDir()
	viewer, err := brainViewerStart(envOf(t))("claude-a", filepath.Join(dir, brain.StreamLogName), dir, []string{"GROQ_API_KEY"})
	if err != nil || viewer == nil {
		t.Fatalf("no viewer started: %v", err)
	}
	if !viewer.IsAlive() {
		t.Fatal("the viewer is not alive")
	}
	viewer.Close()

	calls := []string{"tmux.kill-session", "tmux.new-session", "tmux.has-session"}
	if runtime.GOOS == "darwin" {
		calls = append(calls, "osascript.-e")
	}
	for _, call := range calls {
		t.Run(call, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(logs, call))
			if err != nil {
				t.Fatal(err)
			}
			env := strings.Split(string(data), "\n")
			for _, want := range []string{"HOME=" + homeOf(t), "TMUX_TMPDIR=" + filepath.Join(logs, "tmux"), "GROQ_API_KEY=test-groq-key"} {
				if !slices.Contains(env, want) {
					t.Errorf("%s lacks %s", call, want)
				}
			}
			for _, kv := range env {
				for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "FRIDOLIN_TEST_SECRET"} {
					if strings.HasPrefix(kv, k+"=") {
						t.Errorf("%s got %s", call, kv)
					}
				}
			}
		})
	}
}

// TestViewerRunsOnlySystemPrograms: the viewer's tmux, the sh and tail it
// runs by name and the osascript that opens Terminal all run outside the
// sandbox, so none of them may come from a PATH folder a brain can write
// to. Terminal is opened with /usr/bin/osascript, and every tmux and
// osascript call gets platform.SystemPATH and no other PATH, whatever the viewer's
// environment says.
func TestViewerRunsOnlySystemPrograms(t *testing.T) {
	// Serial: replaces tmuxPath and osascript, package variables, and
	// changes the process's PATH and HOME.
	if osascript != "/usr/bin/osascript" {
		t.Errorf("osascript = %q, want /usr/bin/osascript", osascript)
	}
	planted, home := plantedOutsideHome(t)
	logs, fakes := t.TempDir(), t.TempDir()
	// Each call records its environment under its first argument.
	fake := "#!/bin/sh\nenv > \"" + logs + "/$(basename \"$0\").$1\"\n"
	for _, name := range []string{"tmux", "osascript"} {
		if err := os.WriteFile(filepath.Join(fakes, name), []byte(fake), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	forgetTmux(t)
	tmuxOnce.Do(func() { tmuxPath = filepath.Join(fakes, "tmux") })
	oldOsascript := osascript
	osascript = filepath.Join(fakes, "osascript")
	t.Cleanup(func() { osascript = oldOsascript })

	tv := &platform.Viewer{TmuxPath: findTmux(), Osascript: osascript, Timeout: viewerTimeout, Name: "claude-a", StreamLog: filepath.Join(home, brain.StreamLogName), CWD: home,
		Env: []string{"HOME=" + home, "PATH=" + planted + ":/usr/bin:/bin", "TMUX_TMPDIR=" + filepath.Join(logs, "tmux")}}
	if err := tv.Start(); err != nil {
		t.Fatal(err)
	}
	if !tv.IsAlive() {
		t.Fatal("the viewer is not alive")
	}
	tv.Close()

	calls := []string{"tmux.kill-session", "tmux.new-session", "tmux.has-session"}
	if runtime.GOOS == "darwin" {
		calls = append(calls, "osascript.-e")
	}
	for _, call := range calls {
		t.Run(call, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(logs, call))
			if err != nil {
				t.Fatal(err)
			}
			var paths []string
			for _, kv := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(kv, "PATH=") {
					paths = append(paths, kv)
				}
			}
			if want := "PATH=" + platform.SystemPATH; len(paths) != 1 || paths[0] != want {
				t.Errorf("%s ran with %q, want only %s", call, paths, want)
			}
		})
	}
	for _, name := range shadowingPrograms {
		if _, err := os.Stat(filepath.Join(planted, name+".ran")); err == nil {
			t.Errorf("the %s planted on PATH ran", name)
		}
	}
}

// TestViewerWithRealTmux runs the viewer on a tmux server of its own: a
// folder name a shell would read as quotes, a format or a command goes
// through untouched, and a session whose name merely starts with the
// viewer's is neither taken for it nor replaced. The session's sh and tail
// are the system's, not the ones first on the viewer's PATH.
func TestViewerWithRealTmux(t *testing.T) {
	// Serial: replaces tmuxPath and osascript, package variables, and sets
	// the process's TMUX_TMPDIR.
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("TMUX_TMPDIR", tmuxSocketDir(t))
	env := childEnv(envOf(t), anyChild, nil)
	tmuxCmd := func(args ...string) *exec.Cmd {
		cmd := exec.Command(tmux, args...)
		cmd.Env = env
		return cmd
	}
	t.Cleanup(func() { _ = tmuxCmd("kill-server").Run() })
	bin := t.TempDir()
	attachLog := filepath.Join(bin, "osascript.log")
	if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte("#!/bin/sh\nfor a; do printf '%s\\n' \"$a\"; done > \""+attachLog+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	tmuxOnce.Do(func() {})
	oldTmux, oldOsascript := tmuxPath, osascript
	tmuxPath, osascript = tmux, filepath.Join(bin, "osascript")
	t.Cleanup(func() { tmuxPath, osascript, tmuxOnce = oldTmux, oldOsascript, sync.Once{} })

	dir := filepath.Join(t.TempDir(), "Alex's 100% $(touch PWNED) `touch PWNED2` brain")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, brain.StreamLogName)
	if err := os.WriteFile(logPath, []byte("hello from the log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := tmuxCmd("new-session", "-d", "-s", "claude-work-archive", "sleep", "60").CombinedOutput(); err != nil {
		t.Fatalf("starting another session: %v %s", err, out)
	}

	// A PATH folder a brain could have planted sh and tail in comes first.
	planted := t.TempDir()
	plantPrograms(t, planted)
	viewerEnv := []string{"PATH=" + planted + ":" + os.Getenv("PATH")}
	for _, kv := range env {
		if !strings.HasPrefix(kv, "PATH=") {
			viewerEnv = append(viewerEnv, kv)
		}
	}
	tv := &platform.Viewer{TmuxPath: findTmux(), Osascript: osascript, Timeout: viewerTimeout, Name: "claude-work", StreamLog: logPath, CWD: dir, Env: viewerEnv}
	if tv.IsAlive() {
		t.Error("claude-work-archive counts as claude-work's viewer")
	}
	if err := tv.Start(); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 10*time.Second, "the log in the viewer", func() bool {
		// A pane's target names the session as "=name:"; "=name" alone
		// is taken for a pane of that name, which there is none of.
		out, _ := tmuxCmd("capture-pane", "-p", "-t", "=claude-work:").Output()
		return strings.Contains(string(out), "hello from the log") && strings.Contains(string(out), "claude-work — Claude activity")
	})
	if !tv.IsAlive() {
		t.Error("the viewer is not alive")
	}
	if err := tmuxCmd("has-session", "-t", "=claude-work-archive").Run(); err != nil {
		t.Error("starting claude-work replaced claude-work-archive")
	}
	for _, p := range []string{filepath.Join(dir, "PWNED"), filepath.Join(dir, "PWNED2"), "PWNED", "PWNED2"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("the folder name ran a command: %s exists", p)
		}
	}
	for _, name := range []string{"sh", "tail"} {
		if _, err := os.Stat(filepath.Join(planted, name+".ran")); err == nil {
			t.Errorf("the session ran the %s planted on its PATH", name)
		}
	}
	if runtime.GOOS == "darwin" {
		data, _ := os.ReadFile(attachLog)
		if !strings.Contains(string(data), shellQuote(tmux)+" attach -t '=claude-work'") || strings.Contains(string(data), "activate") {
			t.Errorf("osascript got %q", data)
		}
	}
	tv.Close()
	if tv.IsAlive() {
		t.Error("Close left the viewer running")
	}
	if err := tmuxCmd("has-session", "-t", "=claude-work-archive").Run(); err != nil {
		t.Error("closing claude-work closed claude-work-archive")
	}
}

// tmuxSocketDir is a TMUX_TMPDIR for a tmux server of the test's own. The
// server's socket is TMUX_TMPDIR/tmux-UID/default, and a socket's path has
// a length limit (104 bytes on macOS): where the test's temp dir is too
// deep for it, a shorter folder is made in TMPDIR, or else in /tmp.
func tmuxSocketDir(t *testing.T) string {
	t.Helper()
	fits := func(dir string) bool {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			dir = real
		}
		return len(filepath.Join(dir, "tmux-"+strconv.Itoa(os.Getuid()), "default")) < 100
	}
	if dir := t.TempDir(); fits(dir) {
		return dir
	}
	for _, base := range []string{"", "/tmp"} {
		dir, err := os.MkdirTemp(base, "tmux-")
		if err != nil {
			continue
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		if fits(dir) {
			return dir
		}
	}
	t.Skip("no folder is short enough for a tmux socket")
	return ""
}
