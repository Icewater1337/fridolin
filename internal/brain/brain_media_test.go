package brain

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/history"
)

func TestBrainClaimsMediaBeforeHistoryWithoutQueueLock(t *testing.T) {
	mgr, _, _ := queueTestManager(t, 0, "")
	br := mgr.Get("a")
	called := 0
	br.claimMedia = func(dir, id string, paths []string) map[string]string {
		called++
		if !br.mu.TryLock() {
			t.Error("media claim runs while the queue lock is held")
		} else {
			br.mu.Unlock()
		}
		if _, err := os.Stat(filepath.Join(br.workDir, history.FileName)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("history existed before media was claimed: %v", err)
		}
		if dir != br.settings.Load().MediaDir || id != br.ID() || len(paths) != 1 || paths[0] != "/incoming/file.txt" {
			t.Errorf("claim input = %q %q %v", dir, id, paths)
		}
		return map[string]string{"/incoming/file.txt": "/claimed/file.txt"}
	}
	m := dm("file")
	m.Media = []string{"/incoming/file.txt"}
	m.Silent = true // record-only chatter does not need a process
	if _, _, err := br.Submit(m); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(br.workDir, history.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if called != 1 || !strings.Contains(string(data), "/claimed/file.txt") || strings.Contains(string(data), "/incoming/file.txt") {
		t.Fatalf("claim calls=%d history=%s", called, data)
	}
}
