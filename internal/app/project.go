package app

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/Icewater1337/fridolin/internal/access"
	"github.com/Icewater1337/fridolin/internal/backup"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
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

// BackupTarget is the Drive backup of br; others are the other brains,
// whose node_policy may name br's folders too.
func BackupTarget(br *brain.Brain, notify func(context.Context, *brain.Brain, string) error, others ...*brain.Brain) backup.Target {
	t := backup.Target{ID: br.ID(), Dir: br.Dir(), WaitIdle: br.WaitIdle, Settings: func() backup.TargetSettings {
		m := br.Manifest()
		// The Drive backup is the cloud: a folder that does not go there,
		// node_only paths, and what the node policy keeps off the cloud
		// stay out of it.
		refs := []vaultBrainRef{{Dir: br.Dir(), Approved: &m}}
		for _, o := range others {
			if o != br {
				om := o.Manifest()
				refs = append(refs, vaultBrainRef{Dir: o.Dir(), Approved: &om})
			}
		}
		b := brainBackup{ID: br.ID(), Dir: filepath.Clean(AbsPath(br.Dir())), policies: backupPolicies(refs, HomeDir())}
		all, _, denied := b.policyDenied(manifest.TargetCloud)
		return backup.TargetSettings{Name: m.Name, Enabled: m.Backup.Cloud() && !all, Exclude: append(m.Backup.CloudExcludes(), denied...)}
	}}
	if notify != nil {
		t.Notify = func(ctx context.Context, text string) error { return notify(ctx, br, text) }
	}
	return t
}

func BackupTargets(brains []*brain.Brain, notify func(context.Context, *brain.Brain, string) error) []backup.Target {
	targets := make([]backup.Target, 0, len(brains))
	for _, br := range brains {
		targets = append(targets, BackupTarget(br, notify, brains...))
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
