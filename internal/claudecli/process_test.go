package claudecli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/testutil"
)

func TestRunCancelsProviderAndGrandchild(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			pidFile, childFile := filepath.Join(dir, "pid"), filepath.Join(dir, "child")
			binary, env := testutil.FakeClaude(t, testutil.Scenario{Provider: provider, Grandchild: true, PIDLog: pidFile, ChildPIDLog: childFile, WaitFile: filepath.Join(dir, "never-release")})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan Result, 1)
			go func() {
				done <- Run(ctx, Invocation{Provider: provider, Run: Spawn{Binary: binary, Dir: dir, Env: append(os.Environ(), env...), WaitDelay: time.Second}, NoticeAfter: time.Hour, ExitGrace: time.Hour}, quietRunCallbacks())
			}()
			readPID := func(path string) int { data, _ := os.ReadFile(path); p, _ := strconv.Atoi(string(data)); return p }
			// The file barrier acknowledges that both processes have started before
			// cancellation. No parser or delivery callback participates in the kill.
			testutil.WaitFor(t, 10*time.Second, "provider and grandchild", func() bool { return readPID(pidFile) > 0 && readPID(childFile) > 0 })
			pid, child := readPID(pidFile), readPID(childFile)
			t.Cleanup(func() { testutil.KillPID(pid); testutil.KillPID(child) })
			cancel()
			select {
			case result := <-done:
				if !errors.Is(result.Err, ErrInterrupted) {
					t.Fatalf("result = %+v; want interrupted", result)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancel did not finish provider supervision")
			}
			testutil.WaitFor(t, 5*time.Second, "provider and grandchild to stop", func() bool { return !processAlive(pid) && !processAlive(child) })
		})
	}
}

// An orphaned zombie no longer runs or holds any pipe, even if its adopter
// has not reaped it yet (common with container PID 1 on Linux).
func processAlive(pid int) bool {
	if !testutil.PIDAlive(pid) {
		return false
	}
	if runtime.GOOS == "windows" {
		return true // PIDAlive sees no ended process there
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && !strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

func TestSharedProviderScenarioKeepsTerminalResults(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			binary, env := testutil.FakeClaude(t, testutil.Scenario{Provider: provider, Result: "recorded answer", AfterOutput: time.Minute})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result := Run(ctx, Invocation{Provider: provider, Run: Spawn{Binary: binary, Dir: t.TempDir(), Env: append(os.Environ(), env...), WaitDelay: time.Second}, NoticeAfter: time.Hour, ExitGrace: 50 * time.Millisecond}, quietRunCallbacks())
			if result.Err != nil || result.Text != "recorded answer" {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}
