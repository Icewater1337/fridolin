package config

import (
	"fmt"
	"time"
)

// SchedulesConfig is the scheduled agents' settings (docs/BRAINS.md,
// "Scheduled agents"): whether they run on their schedules, how they are
// spread, and how much of their history and logs is kept.
type SchedulesConfig struct {
	// Enabled runs the agents on their schedules (default true); Run now
	// works either way.
	Enabled *bool `json:"enabled,omitempty"`
	// JitterMinutes spreads each agent's runs by a fixed offset within
	// this many minutes, unless the agent sets its own (default 2).
	JitterMinutes int `json:"jitter_minutes,omitempty"`
	// KeepDays and KeepRuns bound each agent's history (defaults 30 and
	// 100); MaxTotalMB bounds all their logs together (default 200).
	KeepDays   int `json:"keep_days,omitempty"`
	KeepRuns   int `json:"keep_runs,omitempty"`
	MaxTotalMB int `json:"max_total_mb,omitempty"`
	// MaxLogKB cuts one run's log (default 1024).
	MaxLogKB int `json:"max_log_kb,omitempty"`
}

// SchedulesSettings is the schedules section resolved.
type SchedulesSettings struct {
	Enabled    bool
	Jitter     time.Duration
	KeepDays   int
	KeepRuns   int
	MaxTotalMB int
	MaxLogKB   int
}

// Defaults of the schedules section.
const (
	DefaultScheduleJitterMinutes = 2
	DefaultScheduleKeepDays      = 30
	DefaultScheduleKeepRuns      = 100
	DefaultScheduleMaxTotalMB    = 200
	DefaultScheduleMaxLogKB      = 1024
)

func schedulesSettingsOf(s SchedulesConfig) SchedulesSettings {
	return SchedulesSettings{
		Enabled: s.Enabled == nil || *s.Enabled, Jitter: time.Duration(s.JitterMinutes) * time.Minute,
		KeepDays: s.KeepDays, KeepRuns: s.KeepRuns, MaxTotalMB: s.MaxTotalMB, MaxLogKB: s.MaxLogKB,
	}
}

func schedulesConfigOf(s SchedulesSettings) SchedulesConfig {
	return SchedulesConfig{
		Enabled: new(s.Enabled), JitterMinutes: int(s.Jitter / time.Minute),
		KeepDays: s.KeepDays, KeepRuns: s.KeepRuns, MaxTotalMB: s.MaxTotalMB, MaxLogKB: s.MaxLogKB,
	}
}

func fillSchedulesDefaults(s *SchedulesConfig) {
	if s.Enabled == nil {
		s.Enabled = new(true)
	}
	for _, d := range []struct {
		p *int
		v int
	}{
		{&s.JitterMinutes, DefaultScheduleJitterMinutes},
		{&s.KeepDays, DefaultScheduleKeepDays},
		{&s.KeepRuns, DefaultScheduleKeepRuns},
		{&s.MaxTotalMB, DefaultScheduleMaxTotalMB},
		{&s.MaxLogKB, DefaultScheduleMaxLogKB},
	} {
		if *d.p == 0 {
			*d.p = d.v
		}
	}
}

func (s SchedulesConfig) validate() error {
	for _, n := range []struct {
		name     string
		value    int
		min, max int
	}{
		{"schedules.jitter_minutes", s.JitterMinutes, 1, 24 * 60},
		{"schedules.keep_days", s.KeepDays, 1, 3650},
		{"schedules.keep_runs", s.KeepRuns, 1, 100000},
		{"schedules.max_total_mb", s.MaxTotalMB, 1, 1 << 20},
		{"schedules.max_log_kb", s.MaxLogKB, 1, 1 << 20},
	} {
		if n.value < n.min || n.value > n.max {
			return fmt.Errorf("%s must be between %d and %d, or 0 for the default, not %d", n.name, n.min, n.max, n.value)
		}
	}
	return nil
}
