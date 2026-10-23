package app

import (
	"fmt"
	"strings"
	"time"
)

// encryptionText lists each off-machine store's layers (fridolin vault
// status) and what the last migrate-encryption did.
func encryptionText(st vaultStatus, line func(string, ...any)) {
	if len(st.Encryption) == 0 && len(st.Targets) == 0 {
		return
	}
	line("encryption (docs/VAULT.md, \"Encryption\"):")
	for _, l := range st.Encryption {
		mark := "two layers"
		if len(l.Layers) < 2 {
			mark = "ONE LAYER"
		}
		old := ""
		switch l.Old {
		case "copying":
			old = "; the single-layer copy of before is still there (fridolin vault migrate-encryption)"
		case "retired":
			old = "; the single-layer copy is deleted"
		}
		line("  %-10s %s: %s (%s)%s", l.Store, mark, strings.Join(l.Layers, " + "), l.Where, old)
		if l.Left != "" {
			line("  %-10s by hand: %s", "", l.Left)
		}
	}
	for _, t := range st.Targets {
		switch t.Layers {
		case "restic+crypt":
			line("  %-10s two layers: %s + %s (node target %s)", t.Name, layerCrypt, layerRestic, t.Folder)
		case "restic":
			line("  %-10s ONE LAYER: %s (node target %s): give the target a new, empty folder for both", t.Name, layerRestic, t.Folder)
		}
	}
	if m := st.EncryptionMigration; m != nil {
		line("  last migrate-encryption: %s", migrationSummary(m))
	}
}

func migrationSummary(m *encryptionMigration) string {
	what := "run"
	if m.DryRun {
		what = "dry run"
	}
	state := "ok"
	switch {
	case m.Running:
		state = "running"
	case !m.OK:
		state = "failed"
	}
	return fmt.Sprintf("%s %s, %s", what, m.At.Local().Format("2006-01-02 15:04"), state)
}

// EncryptionMigrationText is fridolin vault migrate-encryption's report
// from the status JSON; done is false while the job runs, or when the
// status holds no run since since.
func EncryptionMigrationText(data []byte, since time.Time) (text string, done bool, err error) {
	st, err := parseVaultStatus(data)
	if err != nil {
		return "", false, err
	}
	m := st.EncryptionMigration
	if m == nil || m.Running || m.At.Before(since.Add(-time.Second)) || st.Job == "migrate-encryption" {
		return "", false, nil
	}
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	line("migrate-encryption: %s", migrationSummary(m))
	for _, s := range m.Stores {
		line("  %-10s %s", s.Store, s.Action)
		if s.Left != "" {
			line("  %-10s by hand: %s", "", s.Left)
		}
		if s.Error != "" {
			line("  %-10s error: %s", "", s.Error)
		}
	}
	for _, t := range st.Targets {
		if t.Layers == "restic" {
			line("  %-10s node target with one layer: its folder keeps the repository of before. Add the target again with a new, empty folder on the node (it gets both layers), take a snapshot, then remove the old one.", t.Name)
		}
	}
	line("  exports    fridolin core export (in the core) writes one with both layers; exports made before stay readable with the X25519 identity (fridolin.age.identity) as long as you keep it.")
	if m.DryRun {
		line("Nothing was changed. fridolin vault migrate-encryption --run copies; add --delete-old to delete the single-layer copies once verified.")
	}
	return b.String(), true, nil
}
