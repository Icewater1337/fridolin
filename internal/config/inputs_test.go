package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResolveUsesInjectedPathsAndPlatform(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	paths := Paths{Home: home, DefaultDataDir: filepath.Join(home, LegacyDataDirName)}
	file := &File{ClaudePath: "~/bin/claude", BrainDirs: []BrainRef{{Path: "~/brain"}}}
	before, _ := json.Marshal(file)
	first, err := resolveFile(file, paths, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if first.DataDir != paths.DefaultDataDir || first.DBPath != filepath.Join(paths.DefaultDataDir, "bridge.db") || first.ClaudePath != filepath.Join(home, "bin", "claude") || first.BrainDirs[0].Path != filepath.Join(home, "brain") {
		t.Fatalf("paths were not derived from injected inputs: %+v", first)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("Resolve created files: %v, %v", entries, err)
	}
	// Choosing a different default on disk cannot change captured inputs.
	if err := os.Mkdir(filepath.Join(home, DataDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	second, err := resolveFile(file, paths, "linux")
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("filesystem changes affected Resolve: %v", err)
	}
	after, _ := json.Marshal(file)
	if string(before) != string(after) {
		t.Fatal("Resolve changed the file layer")
	}
	if got := (BrainRef{Path: "~/brain"}).Dir(home); got != filepath.Join(home, "brain") {
		t.Fatalf("BrainRef.Dir = %q", got)
	}
	for _, goos := range []string{"linux", "windows"} {
		if _, err := resolveFile(&File{Sandbox: SandboxConfig{Enabled: true}}, paths, goos); err == nil {
			t.Fatalf("injected %s platform allowed the macOS sandbox", goos)
		}
	}
	if _, err := resolveFile(&File{Sandbox: SandboxConfig{Enabled: true}}, paths, "darwin"); err != nil {
		t.Fatalf("injected macOS platform: %v", err)
	}
	if _, err := resolveFile(file, Paths{}, "linux"); err == nil || err.Error() != "getting home dir: $HOME is not set" {
		t.Fatalf("missing injected home: %v", err)
	}
}

func TestStoreRetainsInputsAndReadsOnlyRetiredEnvironmentKey(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	paths := Paths{Home: home, DefaultDataDir: filepath.Join(home, LegacyDataDirName)}
	var keys []string
	options := Options{Paths: paths, GOOS: "linux", Getenv: func(key string) string {
		keys = append(keys, key)
		return "true"
	}}
	cs, err := Open(filepath.Join(home, "config.json"), options)
	if err != nil {
		t.Fatal(err)
	}
	options.Paths = Paths{Home: "different", DefaultDataDir: "different"}
	if err := os.Mkdir(filepath.Join(home, DataDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Update("", func(c *File) error { c.Comment = "saved"; return nil }); err != nil {
		t.Fatal(err)
	}
	if cs.Paths() != paths || cs.Get().DataDir != paths.DefaultDataDir || !reflect.DeepEqual(keys, []string{"BRAIN_MODE"}) {
		t.Fatalf("inputs changed: %v %q %v", cs.Paths(), cs.Get().DataDir, keys)
	}
	if _, err := os.Stat(paths.DefaultDataDir); !os.IsNotExist(err) {
		t.Fatalf("Open/Update created the runtime data directory: %v", err)
	}
	options.Paths = paths
	options.Getenv = func(string) string { return "false" }
	if _, err := Open(cs.Path(), options); err == nil || !strings.Contains(err.Error(), "BRAIN_MODE=false") {
		t.Fatalf("retired input was not refused: %v", err)
	}
	options.Getenv = nil
	if _, err := LoadExisting(cs.Path(), options); err != nil {
		t.Fatalf("nil environment lookup must mean no retired switch: %v", err)
	}
}

func TestStarterUsesInjectedPathsAndPlatform(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	paths := Paths{Home: home, DefaultDataDir: filepath.Join(home, LegacyDataDirName)}
	if got := DefaultPath(paths); got != filepath.Join(paths.DefaultDataDir, "config.json") {
		t.Fatalf("DefaultPath = %q", got)
	}
	if !Starter("darwin").Sandbox.Enabled || Starter("linux").Sandbox.Enabled || Starter("windows").Sandbox.Enabled {
		t.Fatal("starter did not use the injected platform")
	}
	path := filepath.Join(home, "requested", "config.json")
	if err := WriteStarter(path, StarterValues{ClaudePath: "claude"}, paths, "linux"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if file.DataDir != "~/"+LegacyDataDirName || file.Sandbox.Enabled {
		t.Fatalf("starter changed chosen inputs: %+v", file)
	}
}

// An access list entry that is not UTF-8 is refused before Clone's JSON
// round trip turns it into U+FFFD, which a sender could then be (#225).
func TestResolveRefusesAccessListsThatAreNotUTF8(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	paths := Paths{Home: home, DefaultDataDir: filepath.Join(home, DataDirName)}
	for name, file := range map[string]*File{
		"allowed_numbers":     {AllowedNumbers: []string{"15551234567", "\xff"}},
		"allowed_groups":      {AllowedGroups: []string{"\xff@g.us"}},
		"group_members":       {GroupMembers: map[string][]string{"120363000000000000@g.us": {"\xff"}}},
		"group_members group": {GroupMembers: map[string][]string{"\xff@g.us": {"15551234567"}}},
	} {
		_, err := resolveFile(file, paths, "linux")
		if err == nil || !strings.Contains(err.Error(), "is not valid UTF-8") {
			t.Errorf("%s: err = %v, want one naming invalid UTF-8", name, err)
		}
	}
	if _, err := resolveFile(&File{AllowedNumbers: []string{"�"}}, paths, "linux"); err != nil {
		t.Errorf("a valid U+FFFD entry: %v", err)
	}
}
