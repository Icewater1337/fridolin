package doctor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
)

// OwnerChecks are the checks of what waits for the owner, in the order
// doctor prints them: approvals not answered, brain folders that changed
// since the owner approved them, and the lockdown.
func OwnerChecks() []Check {
	return []Check{
		{ID: "approvals", Title: "approvals waiting", Run: checkPendingApprovals},
		{ID: "brain-drift", Title: "brain folders as approved", Run: checkBrainDrift},
		{ID: "lockdown", Title: "lockdown", Run: checkLockdown},
	}
}

// The policy's doctor checks (M5) read the audit log, not bridge.db:
// doctor runs beside a bridge that has the database open, and the log
// has every approval asked and decided and every lockdown.

// approvalWarnAfter is how long an approval may wait before doctor warns.
const approvalWarnAfter = time.Hour

// policyLog is what doctor reads of the audit log: the approvals asked
// and not decided, by id with the time they were asked, and the last
// lockdown or unlock.
type policyLog struct {
	pending map[string]time.Time
	lock    *audit.Entry
}

func readPolicyLog(env *Env) (policyLog, error) {
	out := policyLog{pending: map[string]time.Time{}}
	files, err := env.Glob(filepath.Join(env.Config.Boot.Layout().Audit(), "*.jsonl"))
	if err != nil {
		return out, err
	}
	slices.Sort(files)
	if len(files) > 2 {
		files = files[len(files)-2:] // approvals wait at most 30 days
	}
	for _, f := range files {
		data, err := env.ReadFile(f)
		if err != nil {
			return out, err
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var e audit.Entry
			if json.Unmarshal(sc.Bytes(), &e) != nil {
				continue
			}
			switch e.Kind {
			case audit.ApprovalRequested, audit.QuestionAsked:
				if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil {
					out.pending[e.ID] = t
				}
			case audit.ApprovalDecided, audit.QuestionAnswered:
				delete(out.pending, e.ID)
			case audit.Lockdown, audit.Unlock:
				e := e
				out.lock = &e
			}
		}
	}
	return out, nil
}

// checkPendingApprovals warns about approvals that have waited more than
// an hour for the owner's vote.
func checkPendingApprovals(_ context.Context, env *Env) Result {
	if env.Config == nil {
		return NoConfig
	}
	l, err := readPolicyLog(env)
	if err != nil {
		return Result{Status: Warn, Detail: "cannot read the audit log: " + err.Error()}
	}
	if len(l.pending) == 0 {
		return Result{Status: OK, Detail: "none waiting"}
	}
	now := env.Time()
	old, oldest := 0, now
	for _, t := range l.pending {
		if now.Sub(t) > approvalWarnAfter {
			old++
		}
		if t.Before(oldest) {
			oldest = t
		}
	}
	detail := fmt.Sprintf("%d waiting for an answer, the oldest for %s", len(l.pending), now.Sub(oldest).Round(time.Minute))
	if old == 0 {
		return Result{Status: OK, Detail: detail}
	}
	return Result{Status: Warn, Detail: detail,
		Fix: "vote in the polls on WhatsApp, or answer them on the dashboard's Approvals page; no answer counts as no once policy.approval_timeout_hours is over"}
}

// checkLockdown shows whether the bridge is in lockdown.
func checkLockdown(_ context.Context, env *Env) Result {
	if env.Config == nil {
		return NoConfig
	}
	l, err := readPolicyLog(env)
	if err != nil {
		return Result{Status: Warn, Detail: "cannot read the audit log: " + err.Error()}
	}
	if l.lock == nil || l.lock.Kind == audit.Unlock {
		return Result{Status: OK, Detail: "off"}
	}
	return Result{Status: Warn, Detail: fmt.Sprintf("on since %s (by %s): only read-only work runs", l.lock.TS, l.lock.By),
		Fix: "send !unlock from an allowed number, or unlock it on the dashboard's Approvals page"}
}
