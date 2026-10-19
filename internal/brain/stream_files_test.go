package brain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/fsx"
)

// TestStreamLogIsNotFollowedOutOfTheFolder: every run appends what the
// brain did to claude-stream.log. Swapped for a symlink, the log is not
// written through, so a brain cannot have the bridge write to a file its
// sandbox keeps it from writing; the run itself goes on.
func TestStreamLogIsNotFollowedOutOfTheFolder(t *testing.T) {
	const stream = `{"type":"result","result":"done"}` + "\n"
	cases := []struct {
		name   string
		target func(home string) string // what the log points to; nil for none yet
	}{
		{"no log yet", nil},
		{"symlink to a file outside", func(home string) string { return filepath.Join(home, ".ssh", "id_test") }},
		{"symlink inside the folder", func(string) string { return "notes.md" }},
		{"dangling symlink out", func(home string) string { return filepath.Join(home, ".ssh", "authorized_keys") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br, home := folderBrain(t)
			dir := filepath.Join(home, "a")
			notes := filepath.Join(dir, "notes.md")
			if err := os.WriteFile(notes, []byte("NOTES\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(dir, StreamLogName)
			if tc.target != nil {
				plant(t, logPath, tc.target(home))
			}
			logged := captureLog(t)
			if res := readTestStream(br, Inbound{Inbound: chat.Inbound{JID: "x"}}, stream); res.Text != "done" {
				t.Errorf("run = %+v, want it to finish", res)
			}
			if tc.target == nil {
				if data, _ := os.ReadFile(logPath); !strings.Contains(string(data), "✅ Result: done") {
					t.Errorf("stream log = %q, want the result", data)
				}
				if lines := logged(); len(lines) > 0 {
					t.Errorf("unexpected log %q", lines)
				}
				return
			}
			if data, _ := os.ReadFile(filepath.Join(home, ".ssh", "id_test")); string(data) != "FAKE-PRIVATE-KEY\n" {
				t.Errorf("the key was written through the symlink: %q", data)
			}
			if data, _ := os.ReadFile(notes); string(data) != "NOTES\n" {
				t.Errorf("notes.md was written through the symlink: %q", data)
			}
			if _, err := os.Lstat(filepath.Join(home, ".ssh", "authorized_keys")); !os.IsNotExist(err) {
				t.Errorf("the dangling symlink's target was created (%v)", err)
			}
			lines := logged()
			if len(lines) != 1 || !strings.Contains(lines[0], "brain a: could not open stream log: "+fsx.ErrSymlink.Error()) {
				t.Errorf("log = %q, want one line refusing the symlink", lines)
			}
		})
	}
}
