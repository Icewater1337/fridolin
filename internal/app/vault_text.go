package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// ReadVaultStatusFile reads what the vault last wrote to
// core/vault-status.json in dataDir, for fridolin vault status and doctor
// when no bridge answers.
func ReadVaultStatusFile(dataDir string, read func(string) ([]byte, error)) ([]byte, error) {
	data, err := read(filepath.Join(dataDir, "core", vaultStatusFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("the vault has written no status yet: it has not run on this data dir")
	}
	return data, err
}

func parseVaultStatus(data []byte) (vaultStatus, error) {
	var st vaultStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("reading the vault's status: %w", err)
	}
	return st, nil
}

// gib formats n bytes for people.
func gib(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f kB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return now.Sub(t).Round(time.Second).String() + " ago (" + t.Local().Format("2006-01-02 15:04") + ")"
}

// VaultStatusText is fridolin vault status's text for the status JSON.
func VaultStatusText(data []byte, now time.Time) (string, error) {
	st, err := parseVaultStatus(data)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	line("vault: %s", st.State)
	if st.Error != "" {
		line("  error: %s", st.Error)
	}
	if st.State == vaultOff {
		line("  set vault.enabled in config.json to turn it on")
		return b.String(), nil
	}
	if st.Job != "" {
		line("  running now: %s", st.Job)
	}
	line("hot tier: %s of %s budget (eviction aims at %s), %s", gib(st.HotBytes), gib(st.BudgetBytes), gib(st.TargetBytes), st.Root)
	line("cold tier: %s; files hot only %d, cold only %d, both %d", gib(st.ColdBytes), st.Files.Hot, st.Files.Cold, st.Files.Both)
	line("last upload: %s", ago(st.LastUpload, now))
	if r := st.LastSync; r != nil && r.Error != "" {
		line("  last sync failed %s: %s", ago(r.At, now), r.Error)
	}
	if r := st.Reachable; r != nil {
		if r.OK {
			line("cold tier reachable: %s", ago(r.At, now))
		} else {
			line("cold tier NOT reachable %s: %s", ago(r.At, now), r.Error)
		}
	}
	if r := st.LastEvict; r != nil {
		line("last eviction: %s, %d files, %s freed%s", ago(r.At, now), r.Evicted, gib(r.FreedBytes), map[bool]string{true: ", still over budget"}[r.OverBudget])
	}
	sn := st.Snapshots
	if !sn.Enabled {
		line("snapshots: off%s", map[bool]string{true: " — " + sn.Error}[sn.Error != ""])
	} else {
		line("snapshots: %s", sn.Repo)
		for _, r := range []*snapshotRecord{sn.LastNightly, sn.LastHourly} {
			if r == nil {
				continue
			}
			if r.Error != "" && r.ID == "" {
				line("  last %s: failed %s: %s", r.Kind, ago(r.At, now), r.Error)
				continue
			}
			line("  last %s: %s %s, %d files, %s added", r.Kind, r.ID, ago(r.At, now), r.Files, gib(r.BytesAdded))
		}
		if r := sn.LastCheck; r != nil {
			what := "check"
			if r.ReadDataSubset != "" {
				what += " reading " + r.ReadDataSubset
			}
			if r.OK {
				line("  last %s: ok %s", what, ago(r.At, now))
			} else {
				line("  last %s: FAILED %s: %s", what, ago(r.At, now), r.Error)
			}
		}
		if h := sn.DeletionHold; h != nil {
			line("  pruning held since %s: snapshot %s has %d files, the one before %d (allow it on the Vault page)", ago(h.Since, now), h.Snapshot, h.Files, h.PrevFiles)
		}
		if d := sn.Drive; d.LastCopy != nil {
			state := "ok"
			if d.LastCopy.Error != "" {
				state = "failed: " + d.LastCopy.Error
			}
			line("  copy to Google Drive (%s): %s %s", d.Repo, state, ago(d.LastCopy.At, now))
		}
	}
	if l := st.Litestream; l.Enabled {
		if l.Running {
			line("litestream: running (pid %d) for %s, %d restarts, %s", l.PID, now.Sub(l.Since).Round(time.Second), l.Restarts, strings.Join(l.DBs, ", "))
		} else {
			line("litestream: NOT running, %d restarts; %s", l.Restarts, l.LastError)
		}
	}
	if u := st.B2; u != nil {
		line("B2 transactions today (%s UTC): class B %d, class C %d of a daily budget of %d each (%d%%, %d%%); class A (free) %d",
			u.Day, u.Classes["B"], u.Classes["C"], u.Budget, u.Percent["B"], u.Percent["C"], u.Classes["A"])
		if y := u.Yesterday; y != nil {
			line("  the day before: class B %d, class C %d", y["B"], y["C"])
		}
		for _, w := range u.Warnings {
			line("  WARNING: %s", w)
		}
	}
	if d := st.LastDrill; d != nil {
		state := "ok"
		if !d.OK {
			state = "FAILED"
		}
		line("last restore drill: %s %s — %s", state, ago(d.At, now), d.Detail)
		for _, c := range d.Checks {
			mark := "ok  "
			if !c.OK {
				mark = "FAIL"
			}
			line("  %s %s: %s", mark, c.Name, c.Detail)
		}
	}
	if len(st.Evictions) > 0 {
		line("recent evictions:")
		for i, e := range st.Evictions {
			if i == 10 {
				break
			}
			line("  %s  %-9s %s (%s)", e.At.Local().Format("2006-01-02 15:04"), gib(e.Size), e.Path, e.Reason)
		}
	}
	return b.String(), nil
}
