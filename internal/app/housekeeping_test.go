package app

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/housekeeping"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// fakeProcs stands in for the system's process list: signalling a
// process ends it.
type fakeProcs struct {
	mu     sync.Mutex
	procs  []platform.Proc
	env    map[int][]string
	killed []int
}

func (f *fakeProcs) list() ([]platform.Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]platform.Proc(nil), f.procs...), nil
}

func (f *fakeProcs) environ(pid int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.env[pid], nil
}

func (f *fakeProcs) signal(pid int, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, pid)
	for i, p := range f.procs {
		if p.PID == pid {
			f.procs = append(f.procs[:i], f.procs[i+1:]...)
			break
		}
	}
	return nil
}

func (f *fakeProcs) killedPIDs() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.killed...)
}

// useFakeProcs puts procs in place of h's system: the bridge itself, an
// orphaned run with a child, and a heavy process of somebody else.
func useFakeProcs(h *harness) *fakeProcs {
	self := os.Getpid()
	old := time.Now().Add(-3 * time.Hour)
	token := filepath.Join(AbsPath(h.DataDir()), runTokensDirName, "main", "run-gone.token")
	f := &fakeProcs{
		procs: []platform.Proc{
			{PID: self, PPID: 1, Name: "fridolin", Start: old, UID: 501},
			{PID: 7001, PPID: 1, Name: "claude", Args: []string{"claude", "-p"}, Start: old, UID: 501, RSS: 300 << 20},
			{PID: 7002, PPID: 7001, Name: "node", Args: []string{"node", "server.js"}, Start: old, UID: 501},
			{PID: 7100, PPID: 1, Name: "python3", Args: []string{"python3", "train.py"}, Start: old, UID: 501, RSS: 6 << 30},
		},
		env: map[int][]string{7001: {"FRIDOLIN_BRAIN_TOKEN_FILE=" + token}, 7002: {"FRIDOLIN_BRAIN_TOKEN_FILE=" + token}},
	}
	hk := h.b.housekeeping
	hk.list, hk.env, hk.signal = f.list, f.environ, f.signal
	hk.sampleGap, hk.grace = 0, 200*time.Millisecond
	return f
}

func runHousekeeping(t *testing.T, h *harness, dry bool) housekeeping.Report {
	t.Helper()
	w := h.Do(http.MethodPost, "/api/housekeeping/run", map[string]any{"dry_run": dry})
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/housekeeping/run = %d %s", w.Code, w.Body)
	}
	var r housekeeping.Report
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func findingOf(r housekeeping.Report, pid int) (housekeeping.Finding, bool) {
	for _, f := range r.Findings {
		if f.PID == pid {
			return f, true
		}
	}
	return housekeeping.Finding{}, false
}

func TestHousekeepingDryRunByDefaultThenActs(t *testing.T) {
	h := newHarness(t, "")
	f := useFakeProcs(h)

	// The default is a dry run: found, not stopped, nobody told.
	r := runHousekeeping(t, h, false)
	if !r.DryRun {
		t.Fatal("the default run is not a dry run")
	}
	got, ok := findingOf(r, 7001)
	if !ok || got.Kind != housekeeping.KindRunOrphan || got.Outcome != housekeeping.OutcomeWouldStop || len(got.Members) != 2 {
		t.Fatalf("the orphaned run: %+v (found %v)", got, ok)
	}
	if _, ok := findingOf(r, 7100); ok {
		t.Error("natively, somebody else's process is listed")
	}
	if k := f.killedPIDs(); len(k) != 0 {
		t.Fatalf("a dry run signalled %v", k)
	}
	if sent := h.SentTo(harnessDM); len(sent) != 0 {
		t.Fatalf("a dry run told the owner: %q", sent)
	}
	// The report is kept, for the dashboard, the CLI and doctor.
	w := h.Do(http.MethodGet, "/api/housekeeping", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"would stop"`) {
		t.Fatalf("GET /api/housekeeping = %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(h.DataDir(), "core", housekeepingFile)); err != nil {
		t.Fatal(err)
	}

	// With dry_run off it stops the tree, says so once, and audits it.
	writeHousekeepingConfig(t, h, `{"dry_run": false}`)
	r = runHousekeeping(t, h, false)
	got, _ = findingOf(r, 7001)
	if r.DryRun || got.Outcome != housekeeping.OutcomeStopped {
		t.Fatalf("acting run: dry %v, %+v", r.DryRun, got)
	}
	if k := f.killedPIDs(); len(k) != 2 || k[0] != 7001 || k[1] != 7002 {
		t.Fatalf("signalled %v, want the run and its child", k)
	}
	msg := h.WaitSent(harnessDM, "Housekeeping stopped or removed 1")
	if !strings.Contains(msg, "PID 7001") {
		t.Errorf("summary: %q", msg)
	}
	lines, err := audit.Read(filepath.Join(h.DataDir(), audit.DirName), audit.Filter{Kind: audit.Housekeeping})
	if err != nil {
		t.Fatal(err)
	}
	stopped := 0
	for _, l := range lines {
		if l.Outcome == housekeeping.OutcomeStopped {
			stopped++
		}
	}
	if stopped != 1 {
		t.Errorf("%d audit lines of a stop, want 1: %+v", stopped, lines)
	}

	// --dry-run reports only, whatever the setting says.
	if r := runHousekeeping(t, h, true); !r.DryRun {
		t.Error("dry_run in the request was not honoured")
	}
}

func TestHousekeepingLockdownOnlyReports(t *testing.T) {
	h := newHarness(t, `{"housekeeping": {"dry_run": false}}`)
	f := useFakeProcs(h)
	if _, err := h.b.setLockdown(t.Context(), true, "dashboard"); err != nil {
		t.Fatal(err)
	}
	r := runHousekeeping(t, h, false)
	if !r.DryRun || r.Note == "" || len(f.killedPIDs()) != 0 {
		t.Fatalf("in lockdown: dry %v note %q killed %v", r.DryRun, r.Note, f.killedPIDs())
	}
}

// TestHousekeepingAsksInTheContainer: a heavy process that is not the
// bridge's is put to the owner, and stopped once the owner says yes.
func TestHousekeepingAsksInTheContainer(t *testing.T) {
	h := newHarness(t, `{"housekeeping": {"dry_run": false}}`)
	f := useFakeProcs(h)
	h.b.housekeeping.container = true
	r := runHousekeeping(t, h, false)
	got, ok := findingOf(r, 7100)
	if !ok || got.Kind != housekeeping.KindForeign || got.Outcome != housekeeping.OutcomeAsked || got.Approval == "" {
		t.Fatalf("the heavy process: %+v (found %v)", got, ok)
	}
	if k := f.killedPIDs(); len(k) != 2 {
		t.Fatalf("signalled %v before the owner answered, want only the orphaned run", k)
	}
	// Asked once: the next run does not ask again.
	r = runHousekeeping(t, h, false)
	if again, _ := findingOf(r, 7100); again.Approval != got.Approval {
		t.Fatalf("asked again: %+v", again)
	}
	w := h.Do(http.MethodPost, "/api/approvals/"+got.Approval, map[string]string{"decision": "approve"})
	if w.Code != http.StatusOK {
		t.Fatalf("approve = %d %s", w.Code, w.Body)
	}
	h.WaitFor("the approved stop", func() bool {
		k := f.killedPIDs()
		return len(k) == 3 && k[2] == 7100
	})
	h.WaitSent(harnessDM, "Housekeeping stopped PID 7100")
}

// writeHousekeepingConfig sets the housekeeping section through the
// dashboard's config API.
func writeHousekeepingConfig(t *testing.T, h *harness, section string) {
	t.Helper()
	v := h.Config()
	var cfg map[string]any
	if err := json.Unmarshal(v.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	var hk map[string]any
	if err := json.Unmarshal([]byte(section), &hk); err != nil {
		t.Fatal(err)
	}
	cfg["housekeeping"] = hk
	if w := h.Do(http.MethodPut, "/api/config", map[string]any{"etag": v.ETag, "config": cfg}); w.Code != http.StatusOK {
		t.Fatalf("PUT /api/config = %d %s", w.Code, w.Body)
	}
}
