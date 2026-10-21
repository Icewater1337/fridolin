package config

import (
	"fmt"
	"time"
)

// HousekeepingConfig is the housekeeping job (internal/housekeeping): it
// looks for what the bridge and its children left running, and for
// processes that hold CPU or memory, every EveryHours, and stops what is
// the bridge's own. On by default, in dry run: it only reports until
// dry_run is set to false.
type HousekeepingConfig struct {
	// Enabled runs the job on its schedule (default true). It can be run
	// by hand either way.
	Enabled *bool `json:"enabled,omitempty"`
	// EveryHours is how often it runs (default 48).
	EveryHours int `json:"every_hours,omitempty"`
	// DryRun only reports what it would stop or remove (default true).
	DryRun *bool `json:"dry_run,omitempty"`
	// CPUPercent and RSSMB make a process heavy: over either, a live
	// process of the bridge is reported and, in the core's container, a
	// process that is not the bridge's is put to the owner (defaults 80
	// and 2048).
	CPUPercent int `json:"cpu_percent,omitempty"`
	RSSMB      int `json:"rss_mb,omitempty"`
	// MinAgeMinutes leaves processes younger than this alone (default 60).
	MinAgeMinutes int `json:"min_age_minutes,omitempty"`
}

// HousekeepingSettings is the housekeeping section resolved.
type HousekeepingSettings struct {
	Enabled    bool
	Every      time.Duration
	DryRun     bool
	CPUPercent int
	RSSMB      int
	MinAge     time.Duration
}

func housekeepingSettingsOf(h HousekeepingConfig) HousekeepingSettings {
	return HousekeepingSettings{
		Enabled: h.Enabled == nil || *h.Enabled, DryRun: h.DryRun == nil || *h.DryRun,
		Every:      time.Duration(h.EveryHours) * time.Hour,
		CPUPercent: h.CPUPercent, RSSMB: h.RSSMB,
		MinAge: time.Duration(h.MinAgeMinutes) * time.Minute,
	}
}

func housekeepingConfigOf(h HousekeepingSettings) HousekeepingConfig {
	return HousekeepingConfig{
		Enabled: new(h.Enabled), DryRun: new(h.DryRun),
		EveryHours: int(h.Every / time.Hour), CPUPercent: h.CPUPercent, RSSMB: h.RSSMB,
		MinAgeMinutes: int(h.MinAge / time.Minute),
	}
}

// Defaults of the housekeeping job.
const (
	DefaultHousekeepingEveryHours    = 48
	DefaultHousekeepingCPUPercent    = 80
	DefaultHousekeepingRSSMB         = 2048
	DefaultHousekeepingMinAgeMinutes = 60
)

func fillHousekeepingDefaults(h *HousekeepingConfig) {
	if h.Enabled == nil {
		h.Enabled = new(true)
	}
	if h.DryRun == nil {
		h.DryRun = new(true)
	}
	for _, d := range []struct {
		p *int
		v int
	}{
		{&h.EveryHours, DefaultHousekeepingEveryHours},
		{&h.CPUPercent, DefaultHousekeepingCPUPercent},
		{&h.RSSMB, DefaultHousekeepingRSSMB},
		{&h.MinAgeMinutes, DefaultHousekeepingMinAgeMinutes},
	} {
		if *d.p == 0 {
			*d.p = d.v
		}
	}
}

func (h HousekeepingConfig) validate() error {
	for _, n := range []struct {
		name     string
		value    int
		min, max int
	}{
		{"housekeeping.every_hours", h.EveryHours, 1, 24 * 365},
		{"housekeeping.cpu_percent", h.CPUPercent, 1, 100 * 1024},
		{"housekeeping.rss_mb", h.RSSMB, 1, 1 << 24},
		{"housekeeping.min_age_minutes", h.MinAgeMinutes, 1, 7 * 24 * 60},
	} {
		if n.value < n.min || n.value > n.max {
			return fmt.Errorf("%s must be between %d and %d, or 0 for the default, not %d", n.name, n.min, n.max, n.value)
		}
	}
	return nil
}
