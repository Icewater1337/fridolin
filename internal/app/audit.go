package app

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

// auditDirName is the data dir's folder of audit logs: one JSON line per
// event, a file per month (YYYY-MM.jsonl), see internal/audit.
const auditDirName = audit.DirName

// newAuditLog is the bridge's audit log in its data dir.
func newAuditLog(dataDir string) *audit.Log {
	return &audit.Log{Dir: filepath.Join(AbsPath(dataDir), auditDirName)}
}

// auditFilter reads GET /api/audit's query.
func auditFilter(r *http.Request) audit.Filter {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	months, _ := strconv.Atoi(q.Get("months"))
	return audit.Filter{
		Thread: q.Get("thread"), Kind: q.Get("kind"), Brain: q.Get("brain"), Run: q.Get("run"), ID: q.Get("id"),
		Query: q.Get("q"), Limit: min(max(limit, 0), 2000), Months: min(max(months, 0), 24),
	}
}

// auditLines answers GET /api/audit.
func (b *App) auditLines(_ context.Context, f audit.Filter) ([]audit.Entry, error) {
	return audit.Read(b.audit.Dir, f)
}

// threadView is one thread of GET /api/threads: the chat it is in and
// its runs, newest first.
type threadView = httpapi.ThreadView

// maxThreadRuns caps the runs a thread lists.
const maxThreadRuns = 50

// threads answers GET /api/threads: the runs of the audit log grouped by
// thread, the thread with the newest run first. A run's outcome comes
// from its run_end line, its delegations' runs are in the same thread.
func (b *App) threads(_ context.Context, limit int) ([]threadView, error) {
	lines, err := audit.Read(b.audit.Dir, audit.Filter{Kinds: []string{audit.RunStart, audit.RunEnd, audit.RunRefused}, Limit: 5000})
	if err != nil {
		return nil, err
	}
	ended := map[string]audit.Entry{}
	for _, e := range lines {
		if e.Kind == audit.RunEnd {
			ended[e.Run] = e
		}
	}
	byThread := map[string]*threadView{}
	var order []string
	for _, e := range lines { // newest first
		if e.Kind == audit.RunEnd {
			continue
		}
		t := byThread[e.Thread]
		if t == nil {
			t = &threadView{Thread: e.Thread, Last: e.TS, Runs: []httpapi.ThreadRun{}, Brains: []string{}}
			byThread[e.Thread] = t
			order = append(order, e.Thread)
		}
		if !slices.Contains(t.Brains, e.Brain) {
			t.Brains = append(t.Brains, e.Brain)
		}
		t.Count++
		if len(t.Runs) >= maxThreadRuns {
			continue
		}
		run := httpapi.ThreadRun{Run: e.Run, Brain: e.Brain, Chat: e.Chat, Started: e.TS, Skill: e.Skill, Risk: e.Risk, Detail: e.Detail, Outcome: "running"}
		if e.Kind == audit.RunRefused {
			run.Outcome = "refused: " + e.Outcome
		} else if end, ok := ended[e.Run]; ok {
			run.Outcome, run.DurationMS = end.Outcome, end.DurationMS
		}
		t.Runs = append(t.Runs, run)
	}
	if limit <= 0 {
		limit = 100
	}
	out := []threadView{}
	for _, id := range order[:min(len(order), limit)] {
		out = append(out, *byThread[id])
	}
	return out, nil
}
