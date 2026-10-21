package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
)

// The node targets' doctor check: each target had a snapshot within its
// window. One that did not has a node that is off or away, a disk that is
// unplugged, or a folder that is gone; doctor warns, since the cloud (if
// the brain goes there too) still has a copy.
var targetDoctorChecks = []DoctorCheck{
	{ID: "vault-targets", Title: "node backup targets have a snapshot within their window", Applies: targetsOn, Run: checkVaultTargets},
}

func init() { DoctorRegistry = append(DoctorRegistry, targetDoctorChecks...) }

func targetsOn(env *DoctorEnv) bool { return env.Config != nil && len(env.Config.Vault.Targets) > 0 }

// lastSnapshot is the newest snapshot of st that was made.
func (st targetStatus) lastSnapshot() *snapshotRecord {
	var last *snapshotRecord
	for _, r := range []*snapshotRecord{st.LastNightly, st.LastHourly} {
		if r != nil && r.ID != "" && (last == nil || r.At.After(last.At)) {
			last = r
		}
	}
	return last
}

func checkVaultTargets(_ context.Context, env *DoctorEnv) DoctorResult {
	f := readTargetsFile(env.Config.Layout().Core(), env.ReadFile)
	now := env.now()
	var ok, late []string
	for _, tg := range env.Config.Vault.Targets {
		st := targetStatus{}
		if s := f.Targets[tg.Name]; s != nil {
			st = *s
		}
		if line, good := targetHealth(tg, st, now); good {
			ok = append(ok, line)
		} else {
			late = append(late, line)
		}
	}
	slices.Sort(late)
	if len(late) > 0 {
		return DoctorResult{Status: DoctorWarn,
			Detail: fmt.Sprintf("%d of %d targets have no snapshot within their window: %s", len(late), len(late)+len(ok), strings.Join(late, "; ")),
			Fix:    "is the node on and its agent running (fridolin nodes), its disk plugged in? fridolin vault status shows the last error; the next snapshot runs within the hour once the node is back"}
	}
	return DoctorResult{Status: DoctorOK, Detail: strings.Join(ok, "; ")}
}

// targetHealth says how target tg stands at now, and whether that is
// within its window.
func targetHealth(tg config.VaultTarget, st targetStatus, now time.Time) (string, bool) {
	last := st.lastSnapshot()
	why := ""
	if r := st.Last; r != nil && r.Error != "" {
		why = ": " + r.Error
	}
	if last == nil {
		return tg.Name + " (no snapshot yet" + why + ")", false
	}
	age := now.Sub(last.At)
	if age > tg.Window() {
		return fmt.Sprintf("%s (last snapshot %s, %s ago, window %s%s)", tg.Name, last.ID, age.Round(time.Minute), tg.Window(), why), false
	}
	return fmt.Sprintf("%s: %s snapshot %s, %s ago", tg.Name, last.Kind, last.ID, age.Round(time.Second)), true
}
