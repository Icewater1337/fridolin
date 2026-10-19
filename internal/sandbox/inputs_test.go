package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Independent policy snapshots must use each caller's home/cwd even when
// built concurrently inside the same isolated test process.
func TestProfileUsesExplicitHostPaths(t *testing.T) {
	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			own := filepath.Join(home, "brain")
			if err := os.Mkdir(own, 0700); err != nil {
				t.Fatal(err)
			}
			policy := Policy{Settings: Settings{ExtraDeny: []string{"relative-secret"}}, Context: Context{Home: home, WorkingDir: home, TempDir: filepath.Join(home, "tmp"), TmuxSocket: filepath.Join(home, "tmux")}, WorkDirs: map[string][]string{"a": {own}}}
			profile, err := Profile(policy, "a")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(profile, filepath.Join(home, "relative-secret")) {
				t.Fatal("relative rule did not use supplied working directory")
			}
			if strings.Contains(profile, os.Getenv("HOME")+"/.ssh") {
				t.Fatal("profile read the process HOME instead of the supplied home")
			}
			policy.Context.WorkingDirErr = errors.New("cwd unavailable")
			if _, err := Profile(policy, "a"); err == nil || !strings.Contains(err.Error(), "cwd unavailable") {
				t.Fatalf("lost cwd resolution error: %v", err)
			}
		})
	}
}
