package app

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
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

// TestUndoFromTheChat: the owner's !undo lists the run that wrote a file,
// !undo <run> says what undoing it would do and changes nothing, and
// !undo <run> yes moves the file it created to the folder's trash; the
// dashboard's view then says there is nothing left to undo. Not in a
// group.
func TestUndoFromTheChat(t *testing.T) {
	t.Parallel()
	h := policyHarness(t, `{"group_members": {"120363000000000000@g.us": ["*"]}}`, []testutil.FakeMCPStep{{Write: "notes.md"}})
	dir := filepath.Join(h.home, "taxes")
	if got := h.Answer(inbound{JID: harnessDM, Text: "write it down #mcp"}); !strings.HasPrefix(got, "echo:") {
		t.Fatalf("the run: %q", got)
	}
	run, _ := h.auditOf("run_files")[0]["run"].(string)

	if got := h.Answer(inbound{JID: harnessDM, Text: "!undo"}); !strings.Contains(got, "`"+run+"` — taxes") || !strings.Contains(got, "1 created: notes.md") {
		t.Fatalf("!undo: %q", got)
	}
	got := h.Answer(inbound{JID: harnessDM, Text: "!undo " + run})
	if !strings.Contains(got, "move notes.md to the folder's trash") || !strings.Contains(got, "!undo "+run+" yes") {
		t.Fatalf("!undo %s: %q", run, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.md")); err != nil {
		t.Fatalf("the dry run touched the file: %v", err)
	}
	if got := h.Answer(inbound{JID: harnessGroup, Sender: strangerNumber, Text: "@claude !undo " + run + " yes"}); got != outboundmsg.OwnerCommandGroupNotice {
		t.Errorf("!undo in a group: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.md")); err != nil {
		t.Fatalf("an !undo in a group touched the file: %v", err)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!undo " + run + " yes"}); !strings.HasPrefix(got, "✅") || !strings.Contains(got, "1 file moved to the folder's trash") {
		t.Fatalf("!undo %s yes: %q", run, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.md")); !os.IsNotExist(err) {
		t.Fatalf("notes.md is still there: %v", err)
	}
	trashed, _ := filepath.Glob(filepath.Join(dir, ".fridolin-trash", "*", "notes.md"))
	if len(trashed) != 1 {
		t.Fatalf("notes.md is not in the trash: %v", trashed)
	}
	if lines := h.auditOf("run_files"); len(lines) < 2 || !slices.ContainsFunc(lines, func(l map[string]any) bool {
		return l["by"] == "15551234567" && strings.HasPrefix(l["detail"].(string), "undo: 0 written back, 1 to the trash")
	}) {
		t.Errorf("run_files lines: %v", lines)
	}

	var view struct {
		Record runsnap.Record     `json:"record"`
		Plan   runsnap.UndoResult `json:"plan"`
	}
	w := h.Do("GET", "/api/runs/"+run+"/files", nil)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &view) != nil {
		t.Fatalf("GET files: %d %s", w.Code, w.Body.String())
	}
	if view.Record.Undone == nil || view.Record.Undone.By != "15551234567" || len(view.Plan.Steps) != 1 || view.Plan.Steps[0].Action != runsnap.ActNone {
		t.Errorf("the view after the undo: %s", w.Body.String())
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!undo nope"}); got != outboundmsg.UndoNoRecordReply("nope", 14) {
		t.Errorf("!undo nope: %q", got)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!undo " + run + " now"}); got != outboundmsg.UndoUsage {
		t.Errorf("!undo with a word it does not take: %q", got)
	}
}

// TestUndoFromTheDashboard: a run that changed and deleted files is put
// back through the API, a dry run first; a file changed since is left.
func TestUndoFromTheDashboard(t *testing.T) {
	t.Parallel()
	h := policyHarness(t, "", []testutil.FakeMCPStep{{Write: "notes.md"}, {Write: "summary.md"}})
	dir := filepath.Join(h.home, "taxes")
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "write it down #mcp"}); !strings.HasPrefix(got, "echo:") {
		t.Fatalf("the run: %q", got)
	}
	run, _ := h.auditOf("run_files")[0]["run"].(string)
	// Edited again since the run.
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte("edited by you"), 0o600); err != nil {
		t.Fatal(err)
	}

	var list []map[string]any
	if w := h.Do("GET", "/api/run-files?brain=taxes", nil); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list) != 1 || list[0]["run"] != run {
		t.Fatalf("GET /api/run-files: %d %s", w.Code, w.Body.String())
	}
	var res runsnap.UndoResult
	w := h.Do("POST", "/api/runs/"+run+"/undo", map[string]bool{"dry_run": true})
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil || !res.DryRun {
		t.Fatalf("dry run: %d %s", w.Code, w.Body.String())
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "notes.md")); string(data) == "mine" {
		t.Fatal("the dry run wrote notes.md back")
	}
	w = h.Do("POST", "/api/runs/"+run+"/undo", nil)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.DryRun {
		t.Fatalf("undo: %d %s", w.Code, w.Body.String())
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "notes.md")); string(data) != "mine" {
		t.Errorf("notes.md = %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "summary.md")); string(data) != "edited by you" {
		t.Errorf("summary.md = %q: the undo took back a later edit", data)
	}
	if res.Count(runsnap.ActConflict, false) != 1 || res.Count(runsnap.ActRestore, true) != 1 {
		t.Errorf("result: %s", w.Body.String())
	}
	if w := h.Do("POST", "/api/runs/nope/undo", nil); w.Code != http.StatusNotFound {
		t.Errorf("an unknown run: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("GET", "/api/runs/nope/files", nil); w.Code != http.StatusNotFound {
		t.Errorf("an unknown run's files: %d %s", w.Code, w.Body.String())
	}
	if l := h.auditOf("run_files"); l[len(l)-1]["by"] != "dashboard" {
		t.Errorf("the undo's line: %v", l[len(l)-1])
	}
}
