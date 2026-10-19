//go:build unix

package platform

import (
	"path/filepath"
	"strings"
	"testing"
)

// A viewer is optional: an unavailable helper must fail clearly and must
// never make the caller believe that a monitoring session is running.
func TestViewerWithoutAnAvailableHelper(t *testing.T) {
	t.Parallel()
	for _, helper := range []string{"", filepath.Join(t.TempDir(), "missing-tmux")} {
		t.Run(helper, func(t *testing.T) {
			t.Parallel()
			viewer := &Viewer{Name: "claude-missing", TmuxPath: helper}
			if err := viewer.Start(); err == nil || !strings.Contains(err.Error(), "creating tmux viewer claude-missing:") {
				t.Fatalf("Start = %v; want the failed viewer named", err)
			}
			if viewer.IsAlive() {
				t.Fatal("a failed viewer is reported alive")
			}
			viewer.Close()
		})
	}
}
