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
	file := &Config{ClaudePath: "~/bin/claude", BrainDirs: []BrainRef{{Path: "~/brain"}}}
	before, _ := json.Marshal(file)
	first, err := Resolve(file, paths, "linux")
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
	second, err := Resolve(file, paths, "linux")
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
	if _, err := Resolve(&Config{Sandbox: SandboxConfig{Enabled: true}}, paths, "linux"); err == nil {
		t.Fatal("injected Linux platform allowed the macOS sandbox")
	}
	if _, err := Resolve(&Config{Sandbox: SandboxConfig{Enabled: true}}, paths, "darwin"); err != nil {
		t.Fatalf("injected macOS platform: %v", err)
	}
	if _, err := Resolve(file, Paths{}, "linux"); err == nil || err.Error() != "getting home dir: $HOME is not set" {
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
	if _, err := cs.Update("", func(c *Config) error { c.Comment = "saved"; return nil }); err != nil {
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
	if !Starter("darwin").Sandbox.Enabled || Starter("linux").Sandbox.Enabled {
		t.Fatal("starter did not use the injected platform")
	}
	path := filepath.Join(home, "requested", "config.json")
	if err := WriteStarter(path, "claude", "", paths, "linux"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file Config
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if file.DataDir != "~/"+LegacyDataDirName || file.Sandbox.Enabled {
		t.Fatalf("starter changed chosen inputs: %+v", file)
	}
}
