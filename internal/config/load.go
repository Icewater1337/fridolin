package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Load reads config.json and fills in the built-in defaults. The file
// is the only source of settings: no environment variable or .env overrides
// any of them.
func Load(cfgPath string) (*Config, error) {
	cs, err := Open(cfgPath, nil)
	if err != nil {
		return nil, err
	}
	return cs.Get(), nil
}

// LoadExisting is Load for a command about a bridge that is
// set up: a config file that is not there is an error, not the defaults,
// so that a mistyped --config is reported rather than checked, or used,
// as an empty config.
func LoadExisting(cfgPath string) (*Config, error) {
	if _, err := os.Stat(cfgPath); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no config file at %s (run fridolin init)", cfgPath)
	}
	return Load(cfgPath)
}
