package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A short write must not publish part of a new manifest: a transaction
// cannot roll back a file whose creation never reported success.
func TestNewManifestWriteFailureLeavesNoFile(t *testing.T) {
	const child = "FRIDOLIN_TEST_PARTIAL_MANIFEST"
	if os.Getenv(child) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNewManifestWriteFailureLeavesNoFile$")
		cmd.Env = append(os.Environ(), child+"=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("limited writer: %v\n%s", err, out)
		}
		return
	}
	dir := t.TempDir()
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}
	original := limit
	t.Cleanup(func() {
		// The instrumented child writes its coverage after the test returns.
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
			t.Errorf("restoring file-size limit: %v", err)
		}
	})
	limit.Cur = 1024
	signal.Ignore(syscall.SIGXFSZ)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}
	err := WriteManifest(dir, &BrainManifest{ID: "new", Name: strings.Repeat("n", 4096)}, false)
	if err == nil {
		t.Fatal("manifest write exceeded the file-size limit without failing")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed creation left files: %v, %v", entries, err)
	}
}

// brainViews returns GET /api/brains.
func (h *harness) brainViews() []brainView {
	h.t.Helper()
	var views []brainView
	if w := h.Do("GET", "/api/brains", nil); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &views) != nil {
		h.t.Fatalf("GET /api/brains: %d %s", w.Code, w.Body.String())
	}
	return views
}

// Allowing a group for a brain takes it away from the brain that had it,
// in both brains' brain.json. When the second one cannot be written, the
// first is put back: no brain.json keeps a change config.json never got.
func TestAssignGroupPutsBackTheFirstBrainJSON(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write into a read-only folder")
	}
	h := newHarness(t, "")
	a := h.AddBrain(BrainManifest{ID: "a", Name: "A", Routing: BrainRouting{HandlesDMs: true}})
	b := h.AddBrain(BrainManifest{ID: "b", Name: "B", Routing: BrainRouting{Groups: []string{harnessGroup}}})
	aBefore := readFile(t, filepath.Join(a, ManifestFile))
	bBefore := readFile(t, filepath.Join(b, ManifestFile))
	cfgBefore := readFile(t, filepath.Join(h.Root(), "config.json"))
	// b's brain.json is replaced through a temp file beside it.
	if err := os.Chmod(b, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(b, 0o700) })
	w := h.Do("POST", "/api/chats/allow", chatRequest{JID: harnessGroup, Kind: "group", Brain: "a"})
	if err := os.Chmod(b, 0o700); err != nil {
		t.Fatal(err)
	}
	if w.Code != 422 || !strings.Contains(w.Body.String(), "permission denied") {
		t.Errorf("allow with b's folder read-only: %d %s, want the 422 with the write's error", w.Code, w.Body.String())
	}
	if got := readFile(t, filepath.Join(a, ManifestFile)); got != aBefore {
		t.Errorf("a's brain.json kept the group:\n%s", got)
	}
	if got := readFile(t, filepath.Join(b, ManifestFile)); got != bBefore {
		t.Errorf("b's brain.json changed:\n%s", got)
	}
	if got := readFile(t, filepath.Join(h.Root(), "config.json")); got != cfgBefore {
		t.Errorf("config.json changed:\n%s", got)
	}
	for _, v := range h.brainViews() {
		if v.Status != "ok" {
			t.Errorf("brain %s: %s %+v, want no drift", v.ID, v.Status, v.Issues)
		}
	}
}

// A brain added through the dashboard gets its brain.json with the update
// that registers it. When config.json cannot be written, the add fails and
// leaves no brain.json behind, nor the rest of what it scaffolded.
func TestAFailedAddLeavesNoBrainJSON(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write into a read-only folder")
	}
	h := newHarness(t, "")
	if err := os.Chmod(h.Root(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(h.Root(), 0o700) })
	w := h.Do("POST", "/api/brains", map[string]any{"path": "~/new", "manifest": BrainManifest{ID: "new", Name: "New"}, "create_dir": true})
	if err := os.Chmod(h.Root(), 0o700); err != nil {
		t.Fatal(err)
	}
	if w.Code != 422 {
		t.Errorf("add with config.json not writable: %d %s, want 422", w.Code, w.Body.String())
	}
	if _, err := os.Lstat(filepath.Join(h.Home(), "new")); !os.IsNotExist(err) {
		t.Errorf("the failed add left its folder: %v", err)
	}
	if w := h.Do("POST", "/api/brains", map[string]any{"path": "~/new", "manifest": BrainManifest{ID: "new", Name: "New"}, "create_dir": true}); w.Code != 200 {
		t.Errorf("adding the folder again: %d %s", w.Code, w.Body.String())
	}
	if m, err := ReadManifest(filepath.Join(h.Home(), "new")); err != nil || m.Name != "New" {
		t.Errorf("brain.json after the add: %+v, %v", m, err)
	}
}

// An add writes the brain's brain.json with the update that registers the
// brain, once that update's checks pass, not with the rest of the folder
// before them.
func TestAddWritesBrainJSONWithTheRegistration(t *testing.T) {
	tb := newTestBridge(t, "")
	// Another writer holds the config update while the add scaffolds.
	inside, release, saved := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := tb.cfgs.Update("", func(c *Config) error {
			close(inside)
			<-release
			return nil
		})
		saved <- err
	}()
	<-inside
	added := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		added <- tb.do("POST", "/api/brains", map[string]any{"path": "~/b", "manifest": BrainManifest{ID: "b", Name: "B"}, "create_dir": true})
	}()
	dir := filepath.Join(tb.home, "b")
	waitFor(t, 5*time.Second, "the add's scaffold", func() bool { return fileExists(filepath.Join(dir, "skills", "README.md")) })
	time.Sleep(100 * time.Millisecond) // what the scaffold would write next
	if _, err := os.Lstat(filepath.Join(dir, ManifestFile)); !os.IsNotExist(err) {
		t.Errorf("brain.json is there before the update that registers the brain: %v", err)
	}
	close(release)
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	w := <-added
	var resp struct{ Created []string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &resp) != nil {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	if !slices.Contains(resp.Created, ManifestFile) {
		t.Errorf("created = %v, want brain.json in it", resp.Created)
	}
	if m, err := ReadManifest(dir); err != nil || m.ID != "b" {
		t.Errorf("brain.json after the add: %+v, %v", m, err)
	}
}

// A brain.json of a newer version than this bridge reads, from a newer
// Fridolin before a rollback say, is refused, and no edit writes over it:
// writing it back would drop the settings this version does not know.
func TestANewerBrainJSONIsRefusedAndKept(t *testing.T) {
	h := newHarness(t, "")
	dir := h.AddBrain(BrainManifest{ID: "main", Name: "Main", Routing: BrainRouting{HandlesDMs: true}})
	path := filepath.Join(dir, ManifestFile)
	newer := `{"version": 2, "id": "main", "name": "Main", "routing": {"handles_dms": true}, "voice": {"reply": "audio"}}` + "\n"
	if err := os.WriteFile(path, []byte(newer), 0o600); err != nil {
		t.Fatal(err)
	}
	const why = "version 2 is newer than the version 1 this build of Fridolin reads"

	if _, err := ReadManifest(dir); err == nil || !strings.Contains(err.Error(), why) {
		t.Errorf("ReadManifest: %v, want the versions named", err)
	}
	views := h.brainViews()
	if len(views) != 1 || views[0].Status != "drift" || len(views[0].Issues) != 1 || !strings.Contains(views[0].Issues[0].Detail, why) {
		t.Errorf("GET /api/brains = %+v, want main changed on disk, with the versions named", views)
	}
	writes := []struct {
		name string
		do   func() (int, string)
	}{
		{"saving its settings", func() (int, string) {
			w := h.Do("PUT", "/api/brains/main", map[string]any{"manifest": BrainManifest{ID: "main", Name: "Renamed", Routing: BrainRouting{HandlesDMs: true}}})
			return w.Code, w.Body.String()
		}},
		{"its backup settings", func() (int, string) {
			w := h.Do("PUT", "/api/brains/main/backup", map[string]any{"enabled": true})
			return w.Code, w.Body.String()
		}},
		{"approving it", func() (int, string) {
			var m BrainManifest
			_ = json.Unmarshal([]byte(newer), &m)
			w := h.Do("POST", "/api/brains/main/approve", map[string]any{"manifest": m})
			return w.Code, w.Body.String()
		}},
		{"WriteManifest", func() (int, string) {
			err := WriteManifest(dir, &BrainManifest{ID: "main", Name: "Main"}, true)
			if err == nil {
				return 200, ""
			}
			return 422, err.Error()
		}},
	}
	for _, wr := range writes {
		t.Run(wr.name, func(t *testing.T) {
			if code, body := wr.do(); code != 422 || !strings.Contains(body, why) {
				t.Errorf("%d %s, want a 422 that names the versions", code, body)
			}
			if got := readFile(t, path); got != newer {
				t.Errorf("brain.json was written over:\n%s", got)
			}
		})
	}
	if got := h.Approved()["main"].Name; got != "Main" {
		t.Errorf("config.json approves %q", got)
	}
}

// A field brain.json has but this version does not know is logged by its
// path, once, while the brain loads as before.
func TestUnknownBrainJSONFieldsAreLogged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	body := `{"version": 1, "id": "a", "name": "A", "nmae": "x", "routing": {"handles_dm": true, "groups": []}}`
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := captureLog(t)
	for range 3 {
		m, err := ReadManifest(dir)
		if err != nil || m.ID != "a" {
			t.Fatalf("ReadManifest: %+v, %v", m, err)
		}
	}
	var lines []string
	for _, l := range logs() {
		if strings.Contains(l, "ignoring unknown fields") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "[nmae routing.handles_dm]") || !strings.Contains(lines[0], filepath.Join(dir, ManifestFile)) {
		t.Errorf("logged %q, want one line naming nmae and routing.handles_dm", lines)
	}
}

// breakApproved changes brain id's approved manifest in config.json with
// edit, as a hand edit of the file can, past the checks of the dashboard.
func breakApproved(t *testing.T, tb *testBridge, id string, edit func(m *BrainManifest)) {
	t.Helper()
	if _, err := tb.cfgs.Update("", func(c *Config) error {
		for _, ref := range c.BrainDirs {
			if ref.Approved != nil && ref.Approved.ID == id {
				edit(ref.Approved)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A brain that is broken already, its approved manifest invalid say, does
// not block edits and approvals of the other brains with its own error;
// an edit that makes brains clash is still refused.
func TestOneBrokenBrainDoesNotBlockTheOthers(t *testing.T) {
	tb := newTestBridge(t, "")
	a := tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A", Routing: BrainRouting{HandlesDMs: true}})
	tb.addFolderBrain(t, BrainManifest{ID: "b", Name: "B"})
	tb.addFolderBrain(t, BrainManifest{ID: "c", Name: "C", Routing: BrainRouting{Default: true}})
	breakApproved(t, tb, "b", func(m *BrainManifest) { m.Effort = "huge" })
	if _, issues := ResolveBrains(tb.cfgs.Get()); !slices.ContainsFunc(issues, func(is BrainIssue) bool { return is.ID == "b" && is.Kind == "invalid" }) {
		t.Fatalf("b is not invalid: %+v", issues)
	}

	steps := []struct {
		name     string
		do       func() *httptest.ResponseRecorder
		code     int
		contains string
	}{
		{"saving a", func() *httptest.ResponseRecorder {
			return tb.do("PUT", "/api/brains/a", map[string]any{"manifest": BrainManifest{ID: "a", Name: "A2", Routing: BrainRouting{HandlesDMs: true}}})
		}, 200, ""},
		{"approving a's brain.json", func() *httptest.ResponseRecorder {
			m := BrainManifest{ID: "a", Name: "A3", Routing: BrainRouting{HandlesDMs: true}}
			if err := WriteManifest(a, &m, true); err != nil {
				t.Fatal(err)
			}
			return tb.do("POST", "/api/brains/a/approve", map[string]any{"manifest": m})
		}, 200, ""},
		{"allowing a group for a", func() *httptest.ResponseRecorder {
			return tb.do("POST", "/api/chats/allow", chatRequest{JID: "120363000000000000@g.us", Kind: "group", Brain: "a"})
		}, 200, ""},
		{"a clash", func() *httptest.ResponseRecorder {
			return tb.do("PUT", "/api/brains/a", map[string]any{"manifest": BrainManifest{ID: "a", Name: "A3", Routing: BrainRouting{HandlesDMs: true, Default: true}}})
		}, 422, `brains \"a\" and \"c\" are both marked default`},
	}
	for _, st := range steps {
		before := readFile(t, filepath.Join(a, ManifestFile))
		w := st.do()
		if w.Code != st.code || !strings.Contains(w.Body.String(), st.contains) {
			t.Errorf("%s: %d %s, want %d %s", st.name, w.Code, w.Body.String(), st.code, st.contains)
		}
		if st.code != 200 {
			if got := readFile(t, filepath.Join(a, ManifestFile)); got != before {
				t.Errorf("%s: the refused edit changed a's brain.json:\n%s", st.name, got)
			}
		}
	}
	for _, ref := range tb.cfgs.Get().BrainDirs {
		if m := ref.Approved; m.ID == "a" && (m.Name != "A3" || !slices.Contains(m.Routing.Groups, "120363000000000000@g.us") || m.Routing.Default) {
			t.Errorf("a's approved manifest: %+v", *m)
		}
	}
}

// Allowing a group for a brain checks the brain set as an edit of a brain
// does. Here b was left out for claiming a's group, so c had the DMs b
// claims too; moving the group to c would let b run again, and take the
// DMs from c without a word.
func TestAllowingAGroupChecksTheBrains(t *testing.T) {
	const group = "120363000000000000@g.us"
	tb := newTestBridge(t, "")
	a := tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A", Routing: BrainRouting{Groups: []string{group}}})
	b := tb.addFolderBrain(t, BrainManifest{ID: "b", Name: "B"})
	c := tb.addFolderBrain(t, BrainManifest{ID: "c", Name: "C", Routing: BrainRouting{HandlesDMs: true}})
	breakApproved(t, tb, "b", func(m *BrainManifest) { m.Routing = BrainRouting{HandlesDMs: true, Groups: []string{group}} })
	if br := tb.brainMgr.BrainFor("15551234567@s.whatsapp.net", false); br == nil || br.ID() != "c" {
		t.Fatalf("the DMs go to %v, want c", br)
	}
	var before []string
	for _, dir := range []string{a, b, c} {
		before = append(before, readFile(t, filepath.Join(dir, ManifestFile)))
	}
	cfgBefore := readFile(t, tb.cfgs.Path())

	w := tb.do("POST", "/api/chats/allow", chatRequest{JID: group, Kind: "group", Brain: "c"})
	if w.Code != 422 || !strings.Contains(w.Body.String(), `brains \"b\" and \"c\" both handle DMs`) {
		t.Errorf("allow: %d %s, want the 422 for the DMs b and c would both claim", w.Code, w.Body.String())
	}
	for i, dir := range []string{a, b, c} {
		if got := readFile(t, filepath.Join(dir, ManifestFile)); got != before[i] {
			t.Errorf("the refused allow changed %s:\n%s", dir, got)
		}
	}
	if got := readFile(t, tb.cfgs.Path()); got != cfgBefore {
		t.Errorf("the refused allow changed config.json:\n%s", got)
	}
	if br := tb.brainMgr.BrainFor("15551234567@s.whatsapp.net", false); br == nil || br.ID() != "c" {
		t.Errorf("the DMs go to %v, want c still", br)
	}
}
