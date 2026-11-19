package config

import "fmt"

// RunFilesConfig is the record each brain run keeps of the files it
// changed in its folder (internal/runsnap, docs/BRAINS.md "What a run
// changed"): how much a folder may hold for it to be kept, and how long
// the records are. On by default.
type RunFilesConfig struct {
	// Enabled keeps the records (default true).
	Enabled *bool `json:"enabled,omitempty"`
	// MaxFiles and MaxMB are the most files and megabytes a run's folder
	// may hold, less what is left out; a run over either keeps a record
	// that says so, without its files (defaults 20000 and 1024).
	MaxFiles int `json:"max_files,omitempty"`
	MaxMB    int `json:"max_mb,omitempty"`
	// KeepDays drops the records of runs older than this (default 14).
	KeepDays int `json:"keep_days,omitempty"`
	// MaxStoreMB drops the oldest records once the stored copies take more
	// than this (default 4096). The copies of what the folders hold now
	// stay whatever it says.
	MaxStoreMB int `json:"max_store_mb,omitempty"`
}

// RunFilesSettings is the run_files section resolved.
type RunFilesSettings struct {
	Enabled    bool
	MaxFiles   int
	MaxMB      int
	KeepDays   int
	MaxStoreMB int
}

// Defaults of run_files.
const (
	DefaultRunFilesMaxFiles   = 20000
	DefaultRunFilesMaxMB      = 1024
	DefaultRunFilesKeepDays   = 14
	DefaultRunFilesMaxStoreMB = 4096
)

func runFilesSettingsOf(c RunFilesConfig) RunFilesSettings {
	return RunFilesSettings{
		Enabled: c.Enabled == nil || *c.Enabled, MaxFiles: c.MaxFiles, MaxMB: c.MaxMB,
		KeepDays: c.KeepDays, MaxStoreMB: c.MaxStoreMB,
	}
}

func runFilesConfigOf(s RunFilesSettings) RunFilesConfig {
	return RunFilesConfig{
		Enabled: new(s.Enabled), MaxFiles: s.MaxFiles, MaxMB: s.MaxMB,
		KeepDays: s.KeepDays, MaxStoreMB: s.MaxStoreMB,
	}
}

func fillRunFilesDefaults(c *RunFilesConfig) {
	if c.Enabled == nil {
		c.Enabled = new(true)
	}
	for _, d := range []struct {
		p *int
		v int
	}{
		{&c.MaxFiles, DefaultRunFilesMaxFiles},
		{&c.MaxMB, DefaultRunFilesMaxMB},
		{&c.KeepDays, DefaultRunFilesKeepDays},
		{&c.MaxStoreMB, DefaultRunFilesMaxStoreMB},
	} {
		if *d.p == 0 {
			*d.p = d.v
		}
	}
}

func (c RunFilesConfig) validate() error {
	for _, n := range []struct {
		name     string
		value    int
		min, max int
	}{
		{"run_files.max_files", c.MaxFiles, 1, 10_000_000},
		{"run_files.max_mb", c.MaxMB, 1, 1 << 24},
		{"run_files.keep_days", c.KeepDays, 1, 3650},
		{"run_files.max_store_mb", c.MaxStoreMB, 1, 1 << 24},
	} {
		if n.value < n.min || n.value > n.max {
			return fmt.Errorf("%s must be between %d and %d, or 0 for the default, not %d", n.name, n.min, n.max, n.value)
		}
	}
	return nil
}
