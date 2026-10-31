package manifest

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/chat"
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
	m := Manifest{}
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
	if err := Write(dir, &m, false); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, &m, false); !errors.Is(err, ErrExists) {
		t.Fatalf("exclusive creation: %v", err)
	}
	if got, err := Read(dir); err != nil || !m.Equal(*got) {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	path := filepath.Join(dir, File)
	future := []byte(`{"version": 99, "id": "work", "name": "Future"}`)
	if err := os.WriteFile(path, future, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); !errors.Is(err, ErrTooNew) {
		t.Fatalf("future read: %v", err)
	}
	if err := Write(dir, &m, true); !errors.Is(err, ErrTooNew) {
		t.Fatalf("future overwrite: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(future) {
		t.Fatalf("future manifest changed: %q, %v", got, err)
	}
}

func leafTestManifest(id string) Manifest {
	return Manifest{ID: id, Name: strings.ToUpper(id[:1]) + id[1:], Backup: Backup{Enabled: true, Exclude: DefaultBackupExcludes()}}
}

func TestManifestValidate(t *testing.T) {
	good := leafTestManifest("work-1")
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(m *Manifest){
		"uppercase id":         func(m *Manifest) { m.ID = "Work" },
		"empty name":           func(m *Manifest) { m.Name = " " },
		"absolute prompt":      func(m *Manifest) { m.PromptFile = "/etc/passwd" },
		"escaping prompt":      func(m *Manifest) { m.PromptFile = "../other/brain-prompt.md" },
		"bad effort":           func(m *Manifest) { m.Effort = "huge" },
		"bad live_session":     func(m *Manifest) { m.LiveSession = "always" },
		"group not a group":    func(m *Manifest) { m.Routing.Groups = []string{"15551234567@s.whatsapp.net"} },
		"filter injection":     func(m *Manifest) { m.Backup.Exclude = []string{"+ /secret"} },
		"newline in exclude":   func(m *Manifest) { m.Backup.Exclude = []string{"a\n+ b"} },
		"github without repos": func(m *Manifest) { m.GitHub = &chat.GitHubGrant{Permissions: map[string]string{"issues": "write"}} },
		"github bad access": func(m *Manifest) {
			m.GitHub = &chat.GitHubGrant{Repos: []string{"you/a"}, Permissions: map[string]string{"issues": "all"}}
		},
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
	if _, err := Read(t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want not-exist", err)
	}
}

func TestManifestObserve(t *testing.T) {
	cases := []struct {
		observe, normalized string
		ok                  bool
	}{
		{"", ObserveHistory, true},
		{ObserveHistory, ObserveHistory, true},
		{ObserveRun, ObserveRun, true},
		{"always", "", false},
		{"Run", "", false},
	}
	for _, c := range cases {
		t.Run(c.observe, func(t *testing.T) {
			m := Manifest{ID: "g", Name: "G", Observe: c.observe}
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
	if !(Manifest{ID: "g", Name: "G"}).Equal(Manifest{ID: "g", Name: "G", Observe: ObserveHistory}) {
		t.Error("a manifest without observe differs from one with the default")
	}
}
