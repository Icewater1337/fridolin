package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
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

// ReadVaultStatusWithTargets is ReadVaultStatusFile with the node targets'
// status (core/vault-targets.json), as far as that file has it, for
// fridolin vault status while no bridge answers.
func ReadVaultStatusWithTargets(dataDir string, read func(string) ([]byte, error)) ([]byte, error) {
	data, err := read(filepath.Join(dataDir, "core", vaultStatusFile))
	tf := readTargetsFile(filepath.Join(dataDir, "core"), read)
	if errors.Is(err, fs.ErrNotExist) {
		if len(tf.Targets) == 0 {
			return nil, errors.New("the vault has written no status yet: it has not run on this data dir")
		}
		data, err = []byte(`{"state":"`+vaultOff+`"}`), nil
	}
	if err != nil || len(tf.Targets) == 0 {
		return data, err
	}
	var st map[string]any
	if json.Unmarshal(data, &st) != nil {
		return data, nil
	}
	var views []targetView
	for _, name := range slices.Sorted(maps.Keys(tf.Targets)) {
		views = append(views, targetView{Name: name, Brains: []string{}, targetStatus: *tf.Targets[name]})
	}
	st["targets"] = views
	return json.Marshal(st)
}

// targetsText is fridolin vault status's lines for the node targets.
func targetsText(ts []targetView, now time.Time, line func(string, ...any)) {
	for _, t := range ts {
		where := t.Folder
		if t.NodeName != "" || t.Node != "" {
			where = fmt.Sprintf("%s on %s (%s)", t.Folder, t.NodeName, t.Node)
		}
		state := ""
		switch {
		case t.Problem != "":
			state = " — " + t.Problem
		case t.Job != "":
			state = " — running now: " + t.Job
		}
		line("target %s: %s%s", t.Name, strings.TrimSpace(where), state)
		if len(t.Brains) > 0 {
			line("  brains: %s", strings.Join(t.Brains, ", "))
		}
		for _, r := range []*snapshotRecord{t.LastNightly, t.LastHourly} {
			if r == nil {
				continue
			}
			if r.Error != "" && r.ID == "" {
				line("  last %s: failed %s: %s", r.Kind, ago(r.At, now), r.Error)
				continue
			}
			line("  last %s: %s %s, %d files, %s added", r.Kind, r.ID, ago(r.At, now), r.Files, gib(r.BytesAdded))
		}
		if !t.SizeAt.IsZero() {
			line("  size: %s in %d files, %s free on its disk (%s)", gib(t.Size), t.Files, gib(int64(min(t.Free, 1<<62))), ago(t.SizeAt, now))
		}
		if r := t.LastCheck; r != nil {
			if r.OK {
				line("  last check: ok %s", ago(r.At, now))
			} else {
				line("  last check: FAILED %s: %s", ago(r.At, now), r.Error)
			}
		}
		if h := t.DeletionHold; h != nil {
			line("  pruning held since %s: snapshot %s has %d files, the one before %d", ago(h.Since, now), h.Snapshot, h.Files, h.PrevFiles)
		}
		if d := t.LastDrill; d != nil {
			state := "ok"
			if !d.OK {
				state = "FAILED"
			}
			line("  last restore drill: %s %s — %s", state, ago(d.At, now), d.Detail)
		}
	}
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
		targetsText(st.Targets, now, line)
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
	if r := st.LastForget; r != nil {
		msg := fmt.Sprintf("last forget: %s, %s: %d files, %s", ago(r.At, now), r.Prefix, r.Forgotten, gib(r.Bytes))
		if r.Kept > 0 {
			msg += fmt.Sprintf(", %d kept (on disk)", r.Kept)
		}
		if r.Error != "" {
			msg += ", failed: " + r.Error
		}
		line("%s", msg)
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
	if sf := st.SecretFolders; sf != nil {
		secretFoldersText(sf, now, line)
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
	targetsText(st.Targets, now, line)
	encryptionText(st, line)
	return b.String(), nil
}

// secretFoldersText is fridolin vault status's lines for the secret
// folders: names, counts and sizes, never a file.
func secretFoldersText(sf *secretFoldersStatus, now time.Time, line func(string, ...any)) {
	if !sf.Enabled {
		line("secret folders: NOT backed up%s", map[bool]string{true: " — " + sf.Error}[sf.Error != ""])
	} else {
		line("secret folders: to %s, %d days of versions kept", sf.Where, sf.KeepDays)
	}
	for _, f := range sf.Folders {
		if f.Error != "" {
			line("  %s (%s): %s", f.Path, f.Source, f.Error)
			continue
		}
		skipped := ""
		if f.Skipped > 0 {
			skipped = fmt.Sprintf(", %d links or special files left out", f.Skipped)
		}
		line("  %s (%s): %d files, %s%s", f.Path, f.Source, f.Files, gib(f.Bytes), skipped)
	}
	if r := sf.LastOK; r != nil {
		line("  last backup: %s %s, %d files, %s (%s)", r.Version, ago(r.At, now), r.Files, gib(r.Bytes), r.Reason)
	} else {
		line("  last backup: never")
	}
	if r := sf.Last; r != nil && r.Error != "" {
		line("  last try FAILED %s: %s", ago(r.At, now), r.Error)
	}
	if n := len(sf.Versions); n > 0 {
		line("  versions: %d, from %s to %s (fridolin vault restore-secrets --list)", n, sf.Versions[0].At.Local().Format("2006-01-02 15:04"), sf.Versions[n-1].At.Local().Format("2006-01-02 15:04"))
	}
	if h := sf.PruneHold; h != nil {
		line("  pruning held since %s: version %s has %d files, the one before %d (allow it on the Vault page)", ago(h.Since, now), h.Snapshot, h.Files, h.PrevFiles)
	}
	if r := sf.LastRestore; r != nil {
		state := "ok"
		if r.Error != "" {
			state = "FAILED: " + r.Error
		}
		line("  last restore: %s %s of %s into %s, %d files", state, ago(r.At, now), r.Version, r.To, r.Files)
	}
}
