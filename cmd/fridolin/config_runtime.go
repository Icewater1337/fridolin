package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// runTimeoutUnit is what one of run_timeout_minutes is, a variable so that
// tests need not wait a minute.
var runTimeoutUnit = time.Minute

// runTimeout is how long a brain's run may take, 0 for no limit.
func runTimeout(c *Config) time.Duration {
	return time.Duration(c.RunMinutes()) * runTimeoutUnit
}

// LoadConfig reads config.json and fills in the built-in defaults. The file
// is the only source of settings: no environment variable or .env overrides
// any of them.
func LoadConfig(cfgPath string) (*Config, error) {
	cs, err := OpenConfigStore(cfgPath)
	if err != nil {
		return nil, err
	}
	return cs.Get(), nil
}

// LoadExistingConfig is LoadConfig for a command about a bridge that is
// set up: a config file that is not there is an error, not the defaults,
// so that a mistyped --config is reported rather than checked, or used,
// as an empty config.
func LoadExistingConfig(cfgPath string) (*Config, error) {
	if _, err := os.Stat(cfgPath); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no config file at %s (run fridolin init)", cfgPath)
	}
	return LoadConfig(cfgPath)
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
func protectDataDir(cfg *Config) {
	// Compare absolute, resolved paths: a relative data_dir such as "."
	// depends on the folder the bridge was started from, and fsx.Within
	// cannot relate it to the home folder, so it would get past the home
	// check.
	dataDir := absPath(cfg.DataDir)
	dbDir := absPath(filepath.Dir(cfg.DBPath))
	home := homeDir()
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

// ownedByOther reports whether path exists and belongs to a user other than
// the one the bridge runs as.
func ownedByOther(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(sys.Uid) != os.Geteuid()
}

// openToOthers returns the permission bits of dir and whether they give
// group or other users any access.
func openToOthers(dir string) (os.FileMode, bool) {
	st, err := os.Stat(dir)
	if err != nil {
		return 0, false
	}
	return st.Mode().Perm(), st.Mode().Perm()&0o077 != 0
}
