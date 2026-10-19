package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/trigger"
)

// Starter is written on the very first run, so the bridge can start and
// everything else can be set up in the dashboard.
func Starter() *config.Config {
	f := false
	return &config.Config{
		Comment:                    "Edit this in the dashboard (fridolin open). Every option: docs/CONFIGURATION.md",
		Version:                    config.Version,
		AllowedNumbers:             []string{},
		AllowAnyDM:                 &f,
		AllowedGroups:              []string{},
		Triggers:                   trigger.FromStrings([]string{"@claude"}),
		DangerouslySkipPermissions: true,
		HTTPListen:                 config.DefaultHTTPListen,
		OTPGuard:                   true,
		Sandbox:                    config.SandboxConfig{Enabled: runtime.GOOS == "darwin", DenyPaths: []string{}, AllowPaths: []string{}},
		// A new install has no scripts written before tokens.
		ScriptAPI: config.ScriptAPIConfig{TokenlessSend: &f},
	}
}

func EnsureFile(path string) error {
	return WriteStarter(path, "", "")
}

// WriteStarter writes the starter config to path unless there is a
// file there. Its data_dir is dataDir, else StarterDataDir's: without one
// every start would pick the data dir again, by which folders exist.
func WriteStarter(path, claudePath, dataDir string) error {
	if _, err := os.Stat(path); err == nil || !os.IsNotExist(err) {
		return err
	}
	cfg := Starter()
	cfg.ClaudePath = claudePath
	cfg.DataDir = dataDir
	if dataDir == "" {
		cfg.DataDir = StarterDataDir(config.HomeDir())
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	log.Printf("No config found — writing a starter config to %s", path)
	return fsx.WriteFileAtomic(path, append(data, '\n'), 0o600, false)
}

// StarterDataDir is the data dir a new config gets when none is given: the
// folder the bridge would pick now, written with ~ so that the config
// still holds when a backup of it is restored under another home folder.
func StarterDataDir(home string) string {
	if _, source := config.ResolveDataDir("", home); source == config.DataDirFromLegacy {
		return "~/" + config.LegacyDataDirName
	}
	return "~/" + config.DataDirName
}

// DefaultPath is the default config file path for the current home.
func DefaultPath() string {
	return filepath.Join(config.DefaultDataDir(config.HomeDir()), "config.json")
}
