package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Layout names the files and folders in the data dir (FRIDOLIN_HOME,
// ~/.fridolin by default), so that no package joins a name onto the data
// dir on its own. It only names paths: whoever uses a folder creates it,
// with EnsureDir, when its feature runs, never the bridge as it starts.
//
// bridge.db and media/ are not here: db_path and media_dir can put them
// elsewhere, so they are Boot.DBPath and Boot.MediaDir.
type Layout struct {
	// Root is the data dir.
	Root string
}

// Layout is the data dir's layout.
func (b Boot) Layout() Layout { return Layout{Root: b.DataDir} }

func (l Layout) join(elem ...string) string {
	return filepath.Join(append([]string{l.Root}, elem...)...)
}

// The files and folders the bridge has today.

// Lock is bridge.lock, which one bridge holds while it uses the data dir.
func (l Layout) Lock() string { return l.join("bridge.lock") }

// DashboardToken is the dashboard's token.
func (l Layout) DashboardToken() string { return l.join("dashboard.token") }

// OwnerApprovals is what the bridge told the owner of what waits for
// their approval, and what they turned down (owner_approvals.go).
func (l Layout) OwnerApprovals() string { return l.join("owner-approvals.json") }

// DashboardSessions holds the dashboard's signed-in browsers.
func (l Layout) DashboardSessions() string { return l.join("dashboard-sessions.json") }

// ScriptsToken is the token scripts send to /send and /ingest.
func (l Layout) ScriptsToken() string { return l.join("scripts.token") }

// Backup is the Drive backup's state.
func (l Layout) Backup() string { return l.join("backup") }

// RcloneConf is the bridge's own rclone config, with the Drive remote.
func (l Layout) RcloneConf() string { return l.join("rclone", "rclone.conf") }

// GoogleClient is the Google OAuth client pasted into Settings.
func (l Layout) GoogleClient() string { return l.join("google", "oauth_client.json") }

// CalendarHealth is the health of the brains' calendar grants: when
// Google last answered each, and whether it refused one since.
func (l Layout) CalendarHealth() string { return l.join("google", "calendar_health.json") }

// DriveClient is a Google OAuth client pasted on the Backup page for the
// Drive link alone; without one the link uses GoogleClient.
func (l Layout) DriveClient() string { return l.join("google", "drive_oauth_client.json") }

// Plaud is the Plaud integration's state.
func (l Layout) Plaud() string { return l.join("plaud") }

// Connections holds the keys of Fridolin's connections to OpenAI and
// ElevenLabs and their usage log (internal/connections).
func (l Layout) Connections() string { return l.join("connections") }

// OTP holds the one-time-code hand-off files, one per brain.
func (l Layout) OTP() string { return l.join("otp") }

// SiteDevices lists the paired phone apps' keys for the site process.
func (l Layout) SiteDevices() string { return l.join("site", "devices.json") }

// The folders of the core, the vault and nodes (docs/PLAN.md).

// Secrets holds age-encrypted secret files.
func (l Layout) Secrets() string { return l.join("secrets") }

// Audit holds the audit log, JSONL files.
func (l Layout) Audit() string { return l.join("audit") }

// VaultDB is the vault's index, one row per file.
func (l Layout) VaultDB() string { return l.join("vault.db") }

// Vault is the root of the hot tier's vault-managed files.
func (l Layout) Vault() string { return l.join("vault") }

// VaultDocuments holds filed documents.
func (l Layout) VaultDocuments() string { return l.join("vault", "documents") }

// VaultWorkspaces holds archived FrIDE workspaces, <task>.tar.zst.
func (l Layout) VaultWorkspaces() string { return l.join("vault", "workspaces") }

// Nodes holds the node registry and the nodes' keys.
func (l Layout) Nodes() string { return l.join("nodes") }

// Tsnet is the embedded Tailscale node's state.
func (l Layout) Tsnet() string { return l.join("tsnet") }

// Core holds the core lease's state and the staging of exports.
func (l Layout) Core() string { return l.join("core") }

// LocalSecrets holds what stays on the machines (internal/gitident): the
// GitLab connection and the git identities' commit names. It lies in
// core/, which no export takes and an import keeps; the restic snapshots
// leave it out too (dataDirExcludes), and a core move carries it sealed
// (internal/secretxfer, LocalDir).
func (l Layout) LocalSecrets() string { return l.join("core", "local-secrets") }

// The memory index and standing intents (M7).

// MemoryIndex is brain id's memory index, derived from its folder and
// rebuilt from it when lost. It is here, not in the brain's state folder
// brains/<id>/, which the sandbox lets the brain write: the index holds
// the provenance of each note, which the brain must not set.
func (l Layout) MemoryIndex(id string) string { return l.join("memory", id+".db") }

// Intents holds brain id's standing intents and the state of its
// schedules, which only the bridge writes.
func (l Layout) Intents(id string) string { return l.join("intents", id+".json") }

// Schedules holds the scheduled agents' state, run history and run logs
// (internal/sched), which only the bridge writes.
func (l Layout) Schedules() string { return l.join("schedules") }

// BrainFolders holds the brains' folders in the core's container, so that
// they move with the volume and its exports (docs/CORE.md). There it is
// where new brain folders go, as the home folder is elsewhere.
func (l Layout) BrainFolders() string { return l.join("brains-folders") }

// EnsureDir creates dir and its parents with mode 0700 if it is missing
// and returns it. A folder that exists keeps its mode.
func EnsureDir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	return dir, nil
}
