package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/config"
)

// The bridge's Backblaze B2 transactions (docs/VAULT.md, "B2
// transactions"): the lease's and the vault's stores, restic and Litestream
// count what they spend in one cloud.Usage per UTC day, which the bridge
// keeps in <data>/core/b2-usage.json each minute for `fridolin doctor` and
// `fridolin vault status`, and takes up again after a restart.

// b2UsageFile is the counts' file in the data dir's core/ folder.
const b2UsageFile = "b2-usage.json"

// b2WarnPercent of the daily budget is where doctor and the dashboard warn.
const b2WarnPercent = 70

// b2UsageEvery is how often the counts are written down.
var b2UsageEvery = time.Minute

// b2UsageRecord is core/b2-usage.json.
type b2UsageRecord struct {
	Today     cloud.UsageDay  `json:"today"`
	Previous  *cloud.UsageDay `json:"previous,omitempty"`
	PID       int             `json:"pid"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// loadB2Usage starts the counts with what an earlier run of the bridge
// kept today.
func loadB2Usage(boot config.Boot) *cloud.Usage {
	u := cloud.NewUsage()
	data, err := os.ReadFile(filepath.Join(boot.Layout().Core(), b2UsageFile))
	if err != nil {
		return u
	}
	var rec b2UsageRecord
	if json.Unmarshal(data, &rec) == nil {
		u.Restore(rec.Today, rec.Previous)
	}
	return u
}

// saveB2Usage writes the counts down, once there are any.
func (b *App) saveB2Usage() {
	if b.b2 == nil {
		return
	}
	today, prev := b.b2.Today()
	if len(today.Ops) == 0 && today.CapExceeded.IsZero() && prev == nil {
		return
	}
	b.writeCoreState(b2UsageFile, b2UsageRecord{Today: today, Previous: prev, PID: os.Getpid(), UpdatedAt: time.Now().UTC()})
}

// runB2Usage writes the counts each minute they changed (Background of
// Start), and once more when ctx ends.
func (b *App) runB2Usage(ctx context.Context) {
	go func() {
		t := time.NewTicker(b2UsageEvery)
		defer t.Stop()
		var last cloud.UsageDay
		for {
			select {
			case <-ctx.Done():
				b.saveB2Usage()
				return
			case <-t.C:
				today, _ := b.b2.Today()
				if usageChanged(last, today) {
					b.saveB2Usage()
					last = today
				}
				b.warnB2Usage(today)
			}
		}
	}()
}

func usageChanged(a, b cloud.UsageDay) bool {
	if a.Day != b.Day || !a.CapExceeded.Equal(b.CapExceeded) || len(a.Ops) != len(b.Ops) {
		return true
	}
	for k, v := range b.Ops {
		if a.Ops[k] != v {
			return true
		}
	}
	return false
}

// warnB2Usage logs once a day per class when the budget's warning level is
// passed, so the log says it before B2 refuses.
func (b *App) warnB2Usage(today cloud.UsageDay) {
	budget := b.cfgs.Get().Vault.B2DailyBudget
	for _, c := range []cloud.Class{cloud.ClassB, cloud.ClassC} {
		n := today.Count(c)
		key := today.Day + string(c)
		if budget <= 0 || n*100 < int64(budget)*b2WarnPercent || b.b2Warned[key] {
			continue
		}
		if b.b2Warned == nil {
			b.b2Warned = map[string]bool{}
		}
		b.b2Warned[key] = true
		log.Printf("warning: %d B2 Class %s transactions today, over %d%% of vault.b2_daily_budget (%d); the free tier refuses past 2,500. Biggest: %s",
			n, c, b2WarnPercent, budget, topOps(today, c, 3))
	}
}

// topOps names the n operations of class c that cost most today.
func topOps(day cloud.UsageDay, c cloud.Class, n int) string {
	type op struct {
		name  string
		count float64
	}
	var ops []op
	prefix := string(c) + " "
	for k, v := range day.Ops {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix && v > 0 {
			ops = append(ops, op{k[len(prefix):], v})
		}
	}
	sort.Slice(ops, func(i, j int) bool {
		return ops[i].count > ops[j].count || ops[i].count == ops[j].count && ops[i].name < ops[j].name
	})
	out := ""
	for i, o := range ops {
		if i == n {
			break
		}
		if out != "" {
			out += ", "
		}
		out += fmt.Sprintf("%s %d", o.name, int64(math.Round(o.count)))
	}
	if out == "" {
		return "none"
	}
	return out
}

// b2UsageView is the counts as the Vault page and fridolin vault status
// show them.
type b2UsageView struct {
	Day    string `json:"day"`
	Budget int    `json:"budget"`
	// Classes are today's counts, rounded; Percent the share of the
	// budget of B and C.
	Classes map[cloud.Class]int64 `json:"classes"`
	Percent map[cloud.Class]int   `json:"percent"`
	// Ops are the counts by class and operation ("C lease:authorize").
	Ops         map[string]int64 `json:"ops,omitempty"`
	CapExceeded time.Time        `json:"cap_exceeded,omitzero"`
	CapError    string           `json:"cap_error,omitempty"`
	// Yesterday is the day before's counts, when this process saw it.
	Yesterday map[cloud.Class]int64 `json:"yesterday,omitempty"`
	Warnings  []string              `json:"warnings,omitempty"`
}

func b2UsageViewOf(today cloud.UsageDay, prev *cloud.UsageDay, budget int) b2UsageView {
	v := b2UsageView{Day: today.Day, Budget: budget, Classes: map[cloud.Class]int64{}, Percent: map[cloud.Class]int{},
		Ops: map[string]int64{}, CapExceeded: today.CapExceeded, CapError: today.CapError}
	for _, c := range []cloud.Class{cloud.ClassA, cloud.ClassB, cloud.ClassC} {
		v.Classes[c] = today.Count(c)
	}
	for k, n := range today.Ops {
		if r := int64(math.Round(n)); r != 0 {
			v.Ops[k] = r
		}
	}
	if prev != nil {
		v.Yesterday = map[cloud.Class]int64{}
		for _, c := range []cloud.Class{cloud.ClassA, cloud.ClassB, cloud.ClassC} {
			v.Yesterday[c] = prev.Count(c)
		}
	}
	v.Warnings = b2Warnings(today, budget)
	if budget > 0 {
		for _, c := range []cloud.Class{cloud.ClassB, cloud.ClassC} {
			v.Percent[c] = int(today.Count(c) * 100 / int64(budget))
		}
	}
	return v
}

// b2Warnings say what is wrong with today's counts: B2 refused a call for
// a cap, or a capped class passed b2WarnPercent of the budget.
func b2Warnings(today cloud.UsageDay, budget int) []string {
	var out []string
	if !today.CapExceeded.IsZero() {
		out = append(out, fmt.Sprintf("Backblaze B2 refused a call at %s UTC: %s. %s.",
			today.CapExceeded.UTC().Format("15:04"), today.CapError, cloud.CapFix))
	}
	if budget <= 0 {
		return out
	}
	for _, c := range []cloud.Class{cloud.ClassB, cloud.ClassC} {
		n := today.Count(c)
		if n*100 >= int64(budget)*b2WarnPercent {
			out = append(out, fmt.Sprintf("%d B2 Class %s transactions today (UTC), %d%% of the daily budget of %d (most: %s): "+
				"B2's free tier refuses every call past 2,500 until midnight UTC. Raise the caps under B2 → Caps & Alerts, or lower the vault's activity.",
				n, c, n*100/int64(budget), budget, topOps(today, c, 3)))
		}
	}
	return out
}

// b2View is the view of the counts, nil while nothing was counted.
func (s *vaultService) b2View() *b2UsageView {
	if s.usage == nil {
		return nil
	}
	today, prev := s.usage.Today()
	if len(today.Ops) == 0 && today.CapExceeded.IsZero() && prev == nil {
		return nil
	}
	v := b2UsageViewOf(today, prev, s.cfgs.Get().Vault.B2DailyBudget)
	return &v
}

// restartKey is the vault's settings that start it again when they
// change: all but the budget, which only changes what is warned about.
func restartKey(v config.VaultConfig) config.VaultConfig {
	v.B2DailyBudget = 0
	return v
}

// ReadB2UsageFile reads core/b2-usage.json in dataDir, for doctor.
func ReadB2UsageFile(dataDir string, read func(string) ([]byte, error)) (b2UsageRecord, error) {
	var rec b2UsageRecord
	data, err := read(filepath.Join(dataDir, "core", b2UsageFile))
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("reading %s: %w", b2UsageFile, err)
	}
	return rec, nil
}
