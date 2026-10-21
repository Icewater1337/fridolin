package app

import (
	"context"
	"encoding/json"

	"github.com/Icewater1337/fridolin/internal/access"
	"github.com/Icewater1337/fridolin/internal/backup"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"
)

// AccessPolicy is the application's read-only projection from a config snapshot.
// The policy never retains or rereads the config store.
func AccessPolicy(cfg *config.Settings) access.Policy {
	return access.Policy{AllowedNumbers: cfg.AllowedNumbers, AllowAnyDM: cfg.AllowAnyDM, AllowedGroups: cfg.AllowedGroups, GroupMembers: cfg.GroupMembers, Triggers: cfg.Triggers}
}

func accessPolicyOf(cfg config.Live) access.Policy {
	return access.Policy{AllowedNumbers: cfg.AllowedNumbers, AllowAnyDM: cfg.AllowAnyDM, AllowedGroups: cfg.AllowedGroups, GroupMembers: cfg.GroupMembers, Triggers: cfg.Triggers}
}

func BackupSettings(settings *config.Settings) backup.Settings {
	cfg := settings.Live
	bc := cfg.Backup
	s := backup.Settings{Enabled: bc.Enabled, Remote: bc.Remote, Root: bc.Root, DailyAt: bc.DailyAt, RclonePath: bc.RclonePath,
		KeepVersionsDays: bc.KeepVersionsDays, IncludeBridgeConfig: bc.IncludeBridgeConfig, IncludeSecretFiles: bc.IncludeSecretFiles}
	for _, ref := range cfg.BrainDirs {
		if ref.Approved != nil {
			s.ConfiguredIDs = append(s.ConfiguredIDs, ref.Approved.ID)
		}
	}
	return s
}

func BackupTarget(br *brain.Brain, notify func(context.Context, *brain.Brain, string) error) backup.Target {
	t := backup.Target{ID: br.ID(), Dir: br.Dir(), WaitIdle: br.WaitIdle, Settings: func() backup.TargetSettings {
		m := br.Manifest()
		// The Drive backup is the cloud: a folder that does not go there,
		// and node_only paths, stay out of it.
		return backup.TargetSettings{Name: m.Name, Enabled: m.Backup.Cloud(), Exclude: m.Backup.CloudExcludes()}
	}}
	if notify != nil {
		t.Notify = func(ctx context.Context, text string) error { return notify(ctx, br, text) }
	}
	return t
}

func BackupTargets(brains []*brain.Brain, notify func(context.Context, *brain.Brain, string) error) []backup.Target {
	targets := make([]backup.Target, 0, len(brains))
	for _, br := range brains {
		targets = append(targets, BackupTarget(br, notify))
	}
	return targets
}

func BackupFiles(cfgs *config.Store) (backup.BridgeFiles, error) {
	file := cfgs.FileCopy()
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return backup.BridgeFiles{}, err
	}
	files := backup.BridgeFiles{Config: data}
	for _, ref := range file.BrainDirs {
		if ref.Approved == nil {
			continue
		}
		md, err := json.MarshalIndent(ref.Approved, "", "  ")
		files.Manifests = append(files.Manifests, backup.ManifestFile{ID: ref.Approved.ID, Data: md, Err: err})
	}
	return files, nil
}

// CredentialsDir is where per-brain credential files live.
func CredentialsDir(cfg *config.Settings) string {
	if cfg.Sandbox.CredentialsDir != "" {
		return cfg.Sandbox.CredentialsDir
	}
	return cfg.DataDir
}

// RuntimeConfig combines live configuration with the process startup values.
// Get/Snapshot retain saved next-start values for the API. Only this copied
// value pins startup settings; slices/maps retain the snapshot's read-only rule.
func RuntimeConfig(cs *config.Store) *config.Settings {
	settings := RuntimeSettings(cs)
	return &settings
}

// RuntimeSettings pins startup resources and permits immediate tightening of
// dashboard authentication. Disabling protection waits for the next start.
func RuntimeSettings(cs *config.Store) config.Settings {
	s := *cs.Get()
	s.Boot = cs.Boot()
	s.Live.Dashboard.Disabled = s.Boot.DashboardDisabled
	s.Live.Dashboard.RequireToken = s.Boot.DashboardRequireToken || s.Live.Dashboard.RequireToken
	return s
}
