package app

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

// writeAgentFile writes dir's .brain/schedules/<name>.json, as a brain
// or the owner does by hand.
func writeAgentFile(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, ".brain", "schedules", name+".json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// listedReview is an agent's review as GET /api/schedules lists it.
type listedReview struct {
	State   string                   `json:"state"`
	File    string                   `json:"file"`
	Risky   []string                 `json:"risky"`
	Problem string                   `json:"problem"`
	Review  *manifest.ScheduleReview `json:"review"`
	Next    *json.RawMessage         `json:"next"`
}

func (h *harness) review(brain, name string) (listedReview, int) {
	h.t.Helper()
	w := h.Do("GET", "/api/schedules", nil)
	var out struct {
		Pending int `json:"pending"`
		Agents  []struct {
			listedReview
			Brain string `json:"brain"`
			Name  string `json:"name"`
		} `json:"agents"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		h.t.Fatalf("GET /api/schedules: %d %s", w.Code, w.Body.String())
	}
	for _, a := range out.Agents {
		if a.Brain == brain && a.Name == name {
			return a.listedReview, out.Pending
		}
	}
	return listedReview{}, out.Pending
}

// TestAgentFilesWaitForApproval: an agent's file that the owner did not
// write through the dashboard (a brain wrote it, or someone by hand) is
// listed as pending with what it may do, runs only once approved as it
// was reviewed, and a change to it, or its removal, waits likewise while
// the approved version runs on.
func TestAgentFilesWaitForApproval(t *testing.T) {
	fakeRuns(t)
	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})

	writeAgentFile(t, dir, "triage", `{"when": "daily 03:00", "prompt": "Triage the issues.", "risk": "irreversible",
		"github": {"repos": ["you/project"], "permissions": {"issues": "write", "contents": "read"}}}`)
	a, pending := h.review("main", "triage")
	if a.State != AgentPending || pending != 1 || a.Review == nil || a.Review.Status != manifest.ReviewNew || a.File != ".brain/schedules/triage.json" || a.Next != nil {
		t.Fatalf("a new agent's file: %+v (pending %d)", a, pending)
	}
	for _, want := range []string{"irreversible", "can write issues on GitHub in you/project"} {
		if !slices.ContainsFunc(a.Risky, func(r string) bool { return strings.Contains(r, want) }) {
			t.Errorf("risky %q lacks %q", a.Risky, want)
		}
	}
	if w := h.Do("POST", "/api/schedules/main/triage/run", nil); w.Code != http.StatusNotFound {
		t.Errorf("run now of an agent not approved: %d", w.Code)
	}

	// Approved only as reviewed.
	other := *a.Review.Disk
	other.When = "daily 04:00"
	if w := h.Do("POST", "/api/schedules/main/triage/approve", map[string]any{"schedule": other}); w.Code != http.StatusConflict {
		t.Errorf("approving another version: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("POST", "/api/schedules/main/triage/approve", map[string]any{"schedule": a.Review.Disk}); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	if got := h.Approved()["main"].Schedules; len(got) != 1 || got[0].When != "daily 03:00" {
		t.Fatalf("approved: %+v", got)
	}
	h.WaitFor("the agent to run", func() bool { a, _ := h.review("main", "triage"); return a.State == AgentActive && a.Review == nil })

	// A change waits; the approved version runs on.
	writeAgentFile(t, dir, "triage", `{"name": "triage", "when": "every 2h", "prompt": "Triage the issues."}`)
	a, pending = h.review("main", "triage")
	if a.State != AgentActive || pending != 1 || a.Review == nil || a.Review.Status != manifest.ReviewChanged || a.Review.Approved.When != "daily 03:00" {
		t.Fatalf("a changed file: %+v", a)
	}
	if w := h.Do("POST", "/api/schedules/main/triage/approve", map[string]any{"schedule": a.Review.Disk}); w.Code != http.StatusOK {
		t.Fatalf("approve the change: %d %s", w.Code, w.Body.String())
	}
	if got := h.Approved()["main"].Schedules; len(got) != 1 || got[0].When != "every 2h" || got[0].GitHub != nil {
		t.Fatalf("approved after the change: %+v", got)
	}

	// brain.json's own approval leaves the agents aside.
	if is := h.Issues(); len(is) != 0 {
		t.Errorf("issues: %+v", is)
	}

	// Its removal waits too, and is approved with no schedule.
	if err := os.Remove(filepath.Join(dir, ".brain", "schedules", "triage.json")); err != nil {
		t.Fatal(err)
	}
	if a, _ = h.review("main", "triage"); a.Review == nil || a.Review.Status != manifest.ReviewRemoved {
		t.Fatalf("a removed file: %+v", a)
	}
	if w := h.Do("POST", "/api/schedules/main/triage/approve", map[string]any{"schedule": nil}); w.Code != http.StatusOK {
		t.Fatalf("approve the removal: %d %s", w.Code, w.Body.String())
	}
	if got := h.Approved()["main"].Schedules; len(got) != 0 {
		t.Fatalf("approved after the removal: %+v", got)
	}

	// A file that cannot be read is listed with why, and cannot be
	// approved.
	writeAgentFile(t, dir, "broken", `{"when": "daily 03:00", "prompt": `)
	a, pending = h.review("main", "broken")
	if a.State != AgentPending || a.Problem == "" || pending != 0 {
		t.Fatalf("a broken file: %+v (pending %d)", a, pending)
	}
	if w := h.Do("POST", "/api/schedules/main/broken/approve", map[string]any{"schedule": map[string]string{"when": "daily 03:00"}}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("approving a broken file: %d %s", w.Code, w.Body.String())
	}
	// Removed by the owner, it goes.
	if w := h.Do("DELETE", "/api/schedules/main/broken", nil); w.Code != http.StatusOK {
		t.Fatalf("delete a pending agent: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".brain", "schedules", "broken.json")); !os.IsNotExist(err) {
		t.Errorf("its file: %v", err)
	}
}

// TestInlineSchedulesMoveIntoFiles: brain.json's schedules of a bridge
// before .brain move into their files as the bridge starts, the rest of
// brain.json kept as it was, and nothing approved changes: an approved
// agent runs on, and a pending one stays pending.
func TestInlineSchedulesMoveIntoFiles(t *testing.T) {
	fakeRuns(t)
	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	if w := h.Do("PUT", "/api/schedules/main/old", map[string]any{"when": "every 6h", "prompt": "Old."}); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	// As an older bridge left it: the agents in brain.json, one of them
	// not approved, and a key this bridge does not know.
	if err := os.RemoveAll(filepath.Join(dir, ".brain")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "brain.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["schedules"] = []map[string]any{
		{"name": "old", "when": "every 6h", "prompt": "Old."},
		{"name": "new", "when": "daily 07:00", "prompt": "New."},
	}
	raw["x_note"] = "kept"
	data, _ = json.MarshalIndent(raw, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "brain.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// Read from brain.json still.
	if a, _ := h.review("main", "new"); a.State != AgentPending || a.File != "brain.json" {
		t.Fatalf("an inline agent: %+v", a)
	}

	h.Restart()
	for _, name := range []string{"old", "new"} {
		if _, err := os.Stat(filepath.Join(dir, ".brain", "schedules", name+".json")); err != nil {
			t.Errorf("%s's file: %v", name, err)
		}
	}
	data, err = os.ReadFile(filepath.Join(dir, "brain.json"))
	if err != nil || strings.Contains(string(data), "schedules") || !strings.Contains(string(data), `"x_note": "kept"`) {
		t.Errorf("brain.json after the move: %s %v", data, err)
	}
	h.WaitFor("the brain", func() bool { a, _ := h.review("main", "old"); return a.State == AgentActive })
	if a, _ := h.review("main", "old"); a.Review != nil || a.File != ".brain/schedules/old.json" {
		t.Errorf("the approved agent after the move: %+v", a)
	}
	if a, pending := h.review("main", "new"); a.State != AgentPending || pending != 1 {
		t.Errorf("the pending agent after the move: %+v", a)
	}
}

// TestRemindersFromTheBrainsFile: the Schedules page lists the reminders
// a brain keeps in its own file, those still to come.
func TestRemindersFromTheBrainsFile(t *testing.T) {
	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	if err := os.WriteFile(filepath.Join(dir, "skills", "reminders.json"), []byte(`[
		{"id": 1, "fire_at": "2099-12-10T06:00:00", "message": "Book the dentist", "sent": false},
		{"id": 2, "fire_at": "2099-10-24T20:00:00", "message": "Stretch", "sent": false, "repeat_every_days": 1},
		{"id": 3, "fire_at": "2020-01-01T10:00:00", "message": "Done long ago", "sent": true}
	]`), 0o600); err != nil {
		t.Fatal(err)
	}
	list := h.schedules()
	var texts []string
	for _, r := range list.Reminders {
		texts = append(texts, r.Text)
	}
	if !slices.Equal(texts, []string{"Stretch", "Book the dentist"}) {
		t.Fatalf("reminders %+v", list.Reminders)
	}
	w := h.Do("GET", "/api/schedules", nil)
	if !strings.Contains(w.Body.String(), `"when":"daily 20:00"`) || !strings.Contains(w.Body.String(), `"source":"skills/reminders.json"`) {
		t.Errorf("a repeating reminder: %s", w.Body.String())
	}
}

// TestSchedulePreview: when a schedule would run next, or why it is no
// schedule.
func TestSchedulePreview(t *testing.T) {
	h := newHarness(t, "")
	var p schedulePreview
	w := h.Do("GET", "/api/schedules/preview?when=daily+07:00", nil)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &p) != nil || len(p.Next) != 3 || p.Error != "" || p.Next[1].Sub(p.Next[0]).Hours() < 23 {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	p = schedulePreview{}
	w = h.Do("GET", "/api/schedules/preview?when=now+and+then", nil)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &p) != nil || len(p.Next) != 0 || p.Error == "" {
		t.Fatalf("preview of no schedule: %d %s", w.Code, w.Body.String())
	}
}
