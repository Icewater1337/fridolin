package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/registry"
)

// logContains reports whether a line captureLog collected contains substr.
func logContains(logs func() []string, substr string) bool {
	for _, l := range logs() {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

// brainFolder makes the folder at path a brain, scaffolded as Add brain does,
// and returns the brain_dirs entry that runs it with m approved. path goes
// into the config as given, and a ~ in it is the test's HOME, so set that
// first. A manifest without a name is named after its id.
func brainFolder(t testing.TB, path string, m manifest.Manifest) config.BrainRef {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if m.Name == "" {
		m.Name = m.ID
	}
	m = m.Normalized()
	if _, err := registry.Scaffold(fsx.ExpandHome(path, home), &m, brainTemplates); err != nil {
		t.Fatal(err)
	}
	return config.BrainRef{Path: path, Approved: &m}
}

// brainDirsJSON is the brain_dirs setting of a test config that runs refs,
// as text to append to its other settings: `, "brain_dirs": [...]`.
func brainDirsJSON(t testing.TB, refs ...config.BrainRef) string {
	t.Helper()
	data, err := json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	return `, "brain_dirs": ` + string(data)
}
