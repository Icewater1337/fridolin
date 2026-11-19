package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/runsnap"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// runRecord reads the record of run's files of brain from the data dir.
func (h *harness) runRecord(brain, run string) *runsnap.Record {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.DataDir(), "run-files", "runs", brain, run+".json"))
	if err != nil {
		h.t.Fatalf("the record of run %s: %v", run, err)
	}
	var rec runsnap.Record
	if err := json.Unmarshal(data, &rec); err != nil {
		h.t.Fatal(err)
	}
	return &rec
}

// TestRunKeepsARecordOfItsFiles: a run that writes a file in its folder
// keeps a record of it, with an audit line of kind run_files; the
// bridge's own files (history, run log) are not in it, and a run that
// changes nothing keeps neither.
func TestRunKeepsARecordOfItsFiles(t *testing.T) {
	t.Parallel()
	h := policyHarness(t, "", []testutil.FakeMCPStep{{Write: "notes.md"}})
	if got := h.Answer(inbound{JID: harnessDM, Text: "write it down #mcp"}); !strings.HasPrefix(got, "echo:") {
		t.Fatalf("the run: %q", got)
	}
	lines := h.auditOf("run_files")
	if len(lines) != 1 || lines[0]["detail"] != "1 created: notes.md" || lines[0]["outcome"] != "ok" || lines[0]["brain"] != "taxes" {
		t.Fatalf("run_files lines: %v", lines)
	}
	run, _ := lines[0]["run"].(string)
	rec := h.runRecord("taxes", run)
	if len(rec.Changes) != 1 || rec.Changes[0].Path != "notes.md" || rec.Changes[0].Op != runsnap.OpCreated || rec.Chat != harnessDM {
		t.Fatalf("record: %+v", rec)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "anything new?"}); !strings.HasPrefix(got, "echo:") {
		t.Fatalf("the second run: %q", got)
	}
	if lines := h.auditOf("run_files"); len(lines) != 1 {
		t.Fatalf("a run that changed nothing has a line: %v", lines)
	}
}

// TestRunFilesOffKeepsNoRecord: with run_files.enabled false no run walks
// its folder.
func TestRunFilesOffKeepsNoRecord(t *testing.T) {
	t.Parallel()
	h := policyHarness(t, `{"run_files": {"enabled": false}}`, []testutil.FakeMCPStep{{Write: "notes.md"}})
	if got := h.Answer(inbound{JID: harnessDM, Text: "write it down #mcp"}); !strings.HasPrefix(got, "echo:") {
		t.Fatalf("the run: %q", got)
	}
	if lines := h.auditOf("run_files"); len(lines) != 0 {
		t.Fatalf("run_files lines: %v", lines)
	}
	if _, err := os.Stat(filepath.Join(h.DataDir(), "run-files")); !os.IsNotExist(err) {
		t.Fatalf("the store was made: %v", err)
	}
}

func TestRunFilesSkip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	skip := runFilesSkip(dir, []string{"/work/", "*.tmp"}, []string{filepath.Join(dir, "private"), dir, filepath.Join(t.TempDir(), "x")})
	for _, tc := range []struct {
		rel  string
		dir  bool
		want bool
	}{
		{"notes.md", false, false},
		{"brain.json", false, true},
		{"sub/brain.json", false, false},
		{"conversation-history.jsonl", false, true},
		{"claude-stream.log", false, true},
		{"claude-stream.1.log", false, true},
		{".git", true, true},
		{"vendor/x/.git", true, true},
		{".fridolin-trash", true, true},
		{"docs/.cold.json", false, true},
		{"work", true, true},
		{"a.tmp", false, true},
		{".env", false, true},
		{"private", true, true},
		{"private/key.md", false, true},
		{"privateer.md", false, false},
		{".claude/settings.json", false, false},
	} {
		if got := skip(tc.rel, tc.dir); got != tc.want {
			t.Errorf("skip(%q, %v) = %v, want %v", tc.rel, tc.dir, got, tc.want)
		}
	}
	if slices.Contains(runFilesBridgeFiles, "") {
		t.Error("an empty name in runFilesBridgeFiles")
	}
}
