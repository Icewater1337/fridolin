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

// auditFiles lists the audit log's monthly files on disk, oldest first.
func auditFiles(env *Env) ([]string, error) {
	files, err := env.Glob(filepath.Join(env.Config.Boot.Layout().Audit(), "*.jsonl"))
	if err != nil {
		return nil, err
	}
	slices.Sort(files)
	return files, nil
}

// eachAuditEntry calls fn with every entry of one monthly file, in order.
func eachAuditEntry(env *Env, file string, fn func(audit.Entry)) error {
	data, err := env.ReadFile(file)
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e audit.Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			fn(e)
		}
	}
	return nil
}

// readPendingApprovals returns the approvals asked and not decided, by id
// with the time they were asked.
func readPendingApprovals(env *Env) (map[string]time.Time, error) {
	pending := map[string]time.Time{}
	files, err := auditFiles(env)
	if err != nil {
		return pending, err
	}
	if len(files) > 2 {
		files = files[len(files)-2:] // approvals wait at most 30 days
	}
	for _, f := range files {
		err := eachAuditEntry(env, f, func(e audit.Entry) {
			switch e.Kind {
			case audit.ApprovalRequested, audit.QuestionAsked:
				if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil {
					pending[e.ID] = t
				}
			case audit.ApprovalDecided, audit.QuestionAnswered:
				delete(pending, e.ID)
			}
		})
		if err != nil {
			return pending, err
		}
	}
	return pending, nil
}

// readLastLock returns the last lockdown or unlock on disk, nil when there
// is none. A lockdown lasts until it is unlocked and is not renewed in the
// log, so this looks back through every month, newest first, until it
// finds one.
func readLastLock(env *Env) (*audit.Entry, error) {
	files, err := auditFiles(env)
	if err != nil {
		return nil, err
	}
	for _, f := range slices.Backward(files) {
		var last *audit.Entry
		err := eachAuditEntry(env, f, func(e audit.Entry) {
			if e.Kind == audit.Lockdown || e.Kind == audit.Unlock {
				last = &e
			}
		})
		if err != nil {
			return nil, err
		}
		if last != nil {
			return last, nil
		}
	}
	return nil, nil
}

// coldAuditMonths counts the audit months the vault's .cold.json lists: they
// are not on disk, and doctor does not bring them back. No listing is none.
func coldAuditMonths(env *Env) int {
	data, err := env.ReadFile(filepath.Join(env.Config.Boot.Layout().Audit(), ".cold.json"))
	if err != nil {
		return 0
	}
	var listing struct {
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	if json.Unmarshal(data, &listing) != nil {
		return 0
	}
	n := 0
	for _, f := range listing.Files {
		if filepath.Ext(f.Name) == ".jsonl" {
			n++
		}
	}
	return n
}

// checkPendingApprovals warns about approvals that have waited more than
// an hour for the owner's vote.
func checkPendingApprovals(_ context.Context, env *Env) Result {
	if env.Config == nil {
		return NoConfig
	}
	pending, err := readPendingApprovals(env)
	if err != nil {
		return Result{Status: Warn, Detail: "cannot read the audit log: " + err.Error()}
	}
	if len(pending) == 0 {
		return Result{Status: OK, Detail: "none waiting"}
	}
	now := env.Time()
	old, oldest := 0, now
	for _, t := range pending {
		if now.Sub(t) > approvalWarnAfter {
			old++
		}
		if t.Before(oldest) {
			oldest = t
		}
	}
	detail := fmt.Sprintf("%d waiting for an answer, the oldest for %s", len(pending), now.Sub(oldest).Round(time.Minute))
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
	lock, err := readLastLock(env)
	if err != nil {
		return Result{Status: Warn, Detail: "cannot read the audit log: " + err.Error()}
	}
	if lock == nil {
		// shortcut: stays OK, not Warn, or every vault install without a lockdown warns for good; read policy_state if doctor ever may open bridge.db.
		if n := coldAuditMonths(env); n > 0 {
			return Result{Status: OK, Detail: fmt.Sprintf("off in the audit log on disk; %d older month(s) are in the vault's cold tier and were not read, the dashboard's Approvals page shows the state", n)}
		}
		return Result{Status: OK, Detail: "off"}
	}
	if lock.Kind == audit.Unlock {
		return Result{Status: OK, Detail: "off"}
	}
	return Result{Status: Warn, Detail: fmt.Sprintf("on since %s (by %s): only read-only work runs", lock.TS, lock.By),
		Fix: "send !unlock from an allowed number, or unlock it on the dashboard's Approvals page"}
}
