package app

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// runTimeoutUnit is what one of run_timeout_minutes is, a variable so that
// tests need not wait a minute.
var runTimeoutUnit = time.Minute

// runTimeout is how long a brain's run may take, 0 for no limit.
func runTimeout(c *config.Settings) time.Duration {
	return c.RunTimeout / time.Minute * runTimeoutUnit
}

// protectDataDir takes group and other access away from the data dir and
// warns about folders it leaves open. MkdirAll gives a new data dir 0700 but
// leaves the mode of an existing one alone, so a legacy folder or one made
// by hand could stay open to every local account. It runs once at startup,
// not in effective(): that also runs on every dashboard save, which would
// change a folder typed into Settings before any restart and repeat the
// warnings on every write.
//
// Three kinds of folder only get a warning: the home folder or one above it
// (data_dir "~"), where other users and services may need access; a data
// dir that belongs to another user, whose mode is theirs to set even when
// the bridge runs as root; and the folder of a db_path outside the data
// dir, which may be shared on purpose. bridge.db itself is 0600 either way.
// A chmod that fails is logged, not fatal, so an upgrade cannot keep a
// working bridge from starting.
func protectDataDir(cfg *config.Settings) {
	// Compare absolute, resolved paths: a relative data_dir such as "."
	// depends on the folder the bridge was started from, and fsx.Within
	// cannot relate it to the home folder, so it would get past the home
	// check.
	dataDir := AbsPath(cfg.DataDir)
	dbDir := AbsPath(filepath.Dir(cfg.DBPath))
	home := HomeDir()
	homeOrAbove := func(dir string) bool {
		return home != "" && fsx.Within(fsx.Canonical(home), fsx.Canonical(dir))
	}

	mode, open := openToOthers(dataDir)
	switch {
	case homeOrAbove(dataDir):
		if open {
			log.Printf("warning: data_dir %s is your home folder or one above it, so its mode %04o stays as it is; a folder of its own, such as ~/.fridolin, keeps the bridge's files private", dataDir, uint32(mode))
		}
	case ownedByOther(dataDir):
		if open {
			log.Printf("warning: data_dir %s belongs to another user, so its mode %04o stays as it is; a folder of your own, such as ~/.fridolin, keeps the bridge's files private", dataDir, uint32(mode))
		}
	default:
		if err := fsx.RestrictToOwner(dataDir); err != nil {
			log.Printf("warning: could not make %s private: %v", dataDir, err)
		}
	}

	if !fsx.Within(fsx.Canonical(dbDir), fsx.Canonical(dataDir)) {
		if mode, open := openToOthers(dbDir); open {
			// Not for the home folder: making it 0700 is what the data dir
			// rule above avoids.
			advice := "; consider chmod 700"
			if homeOrAbove(dbDir) {
				advice = ""
			}
			log.Printf("warning: %s, the folder of db_path, is open to other users (mode %04o)%s", dbDir, uint32(mode), advice)
		}
	}
}

// openConfigStore wires registry validation into each writing store.
func OpenConfigStore(path string) (*config.Store, error) { return config.Open(path, ConfigOptions()) }

// homeDir is the ambient home lookup at the command composition boundary.
func HomeDir() string { home, _ := os.UserHomeDir(); return home }

func ConfigPathsFor(home string) config.Paths {
	return config.Paths{Home: home, DefaultDataDir: config.DefaultDataDir(home)}
}

func ConfigPaths() config.Paths { return ConfigPathsFor(HomeDir()) }

func ConfigOptions() config.Options {
	return config.Options{Paths: ConfigPaths(), GOOS: ConfigGOOS(), Getenv: os.Getenv, CheckBrains: CheckSet}
}

func LoadConfig(path string) (*config.Settings, error) { return config.Load(path, ConfigOptions()) }

func LoadExistingConfig(path string) (*config.Settings, error) {
	return config.LoadExisting(path, ConfigOptions())
}

func EnsureConfigFile(path string) error { return config.EnsureFile(path, ConfigPaths(), ConfigGOOS()) }

// InContainer reports whether this process runs in the fridolin-core image,
// whose Dockerfile sets FRIDOLIN_CONTAINER=1 (docs/CORE.md).
func InContainer() bool { return runtime.GOOS == "linux" && os.Getenv("FRIDOLIN_CONTAINER") == "1" }

// ConfigGOOS is the platform configs are resolved for here: Linux in the
// container is config.ContainerGOOS, where brains are sandboxed with
// bubblewrap.
func ConfigGOOS() string {
	if InContainer() {
		return config.ContainerGOOS
	}
	return runtime.GOOS
}
