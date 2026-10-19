package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestManifestDefaultsAndEnumListsAreOwned(t *testing.T) {
	defaults := DefaultBackupExcludes()
	defaults[0] = "changed"
	if DefaultBackupExcludes()[0] != "node_modules/" {
		t.Fatal("backup defaults share writable state")
	}
	efforts := Efforts()
	efforts[0] = "changed"
	if !ValidEffort("") || ValidEffort("changed") || !slices.Equal(Efforts(), []string{"", "high", "low", "max", "medium", "xhigh"}) {
		t.Fatal("effort list or validation changed")
	}
	modes := ObserveModes()
	modes[0] = "changed"
	if !slices.Equal(ObserveModes(), []string{"", "history", "run"}) {
		t.Fatal("observe modes share writable state")
	}
	m := BrainManifest{}
	n := m.Normalized()
	if n.Routing.Groups == nil || n.Backup.Exclude == nil || m.Routing.Groups != nil || m.Backup.Exclude != nil {
		t.Fatal("normalization changed nil/empty copy behavior")
	}
	m.Routing.Groups = []string{"group@g.us"}
	n = m.Normalized()
	if &n.Routing.Groups[0] != &m.Routing.Groups[0] {
		t.Fatal("normalization unexpectedly copied a non-nil slice")
	}
}

func TestManifestPublicationKeepsTheExistingContracts(t *testing.T) {
	dir := t.TempDir()
	m := leafTestManifest("work")
	if err := WriteManifest(dir, &m, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(dir, &m, false); !errors.Is(err, errManifestExists) {
		t.Fatalf("exclusive creation: %v", err)
	}
	if got, err := ReadManifest(dir); err != nil || !m.Equal(*got) {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	path := filepath.Join(dir, ManifestFile)
	future := []byte(`{"version": 99, "id": "work", "name": "Future"}`)
	if err := os.WriteFile(path, future, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(dir); !errors.Is(err, errManifestTooNew) {
		t.Fatalf("future read: %v", err)
	}
	if err := WriteManifest(dir, &m, true); !errors.Is(err, errManifestTooNew) {
		t.Fatalf("future overwrite: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(future) {
		t.Fatalf("future manifest changed: %q, %v", got, err)
	}
}

func leafTestManifest(id string) BrainManifest {
	return BrainManifest{ID: id, Name: strings.ToUpper(id[:1]) + id[1:], Backup: BrainBackup{Enabled: true, Exclude: DefaultBackupExcludes()}}
}

func TestManifestValidate(t *testing.T) {
	good := leafTestManifest("work-1")
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(m *BrainManifest){
		"uppercase id":       func(m *BrainManifest) { m.ID = "Work" },
		"empty name":         func(m *BrainManifest) { m.Name = " " },
		"absolute prompt":    func(m *BrainManifest) { m.PromptFile = "/etc/passwd" },
		"escaping prompt":    func(m *BrainManifest) { m.PromptFile = "../other/brain-prompt.md" },
		"bad effort":         func(m *BrainManifest) { m.Effort = "huge" },
		"group not a group":  func(m *BrainManifest) { m.Routing.Groups = []string{"15551234567@s.whatsapp.net"} },
		"filter injection":   func(m *BrainManifest) { m.Backup.Exclude = []string{"+ /secret"} },
		"newline in exclude": func(m *BrainManifest) { m.Backup.Exclude = []string{"a\n+ b"} },
	}
	for name, mutate := range bad {
		m := leafTestManifest("work")
		mutate(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func TestReadManifestMissing(t *testing.T) {
	if _, err := ReadManifest(t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want not-exist", err)
	}
}

func TestManifestObserve(t *testing.T) {
	cases := []struct {
		observe, normalized string
		ok                  bool
	}{
		{"", observeHistory, true},
		{observeHistory, observeHistory, true},
		{observeRun, observeRun, true},
		{"always", "", false},
		{"Run", "", false},
	}
	for _, c := range cases {
		t.Run(c.observe, func(t *testing.T) {
			m := BrainManifest{ID: "g", Name: "G", Observe: c.observe}
			if err := m.Validate(); (err == nil) != c.ok {
				t.Fatalf("Validate() = %v, want ok %v", err, c.ok)
			}
			if c.ok && m.Normalized().Observe != c.normalized {
				t.Errorf("normalized observe = %q, want %q", m.Normalized().Observe, c.normalized)
			}
		})
	}
	// A brain.json written before the setting existed is the same brain as
	// its approved copy that has the default spelt out: no drift.
	if !(BrainManifest{ID: "g", Name: "G"}).Equal(BrainManifest{ID: "g", Name: "G", Observe: observeHistory}) {
		t.Error("a manifest without observe differs from one with the default")
	}
}
