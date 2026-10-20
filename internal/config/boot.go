package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Boot holds values captured once when the configuration store opens. It is a
// value copy: live saves keep their requested values in Config for the API and
// next restart, while running components use these paths and startup flags.
type Boot struct {
	HTTPEnabled           bool   `config:"-"`
	Home                  string `config:"-"`
	HTTPListen            string `config:"http_listen"`
	DataDir               string `config:"data_dir"`
	DBPath                string `config:"db_path"`
	MediaDir              string `config:"media_dir"`
	LogLevel              string `config:"log_level"`
	DashboardDisabled     bool   `config:"dashboard.disabled"`
	DashboardRequireToken bool   `config:"dashboard.require_token"`
	NodesEnabled          bool   `config:"nodes.enabled"`
	NodesListen           string `config:"nodes.listen"`
	// The tailnet node and the core lease start with the bridge (tsnet's
	// state, the lease's first write), so they wait for a restart.
	TailnetEnabled    bool   `config:"core.tailnet.enabled"`
	TailnetHostname   string `config:"core.tailnet.hostname"`
	LeaseEnabled      bool   `config:"core.lease.enabled"`
	LeaseRenewSeconds int    `config:"core.lease.renew_seconds"`
	LeaseStaleSeconds int    `config:"core.lease.stale_seconds"`
	// VaultBucket holds the lease (vault.bucket), read as the lease is
	// taken.
	VaultBucket string `config:"vault.bucket"`
}

// Lease returns the lease settings the bridge started with.
func (b Boot) Lease() LeaseConfig {
	return LeaseConfig{Enabled: b.LeaseEnabled, RenewSeconds: b.LeaseRenewSeconds, StaleSeconds: b.LeaseStaleSeconds}
}

func bootOf(c *File, paths Paths) Boot {
	return Boot{
		HTTPEnabled: c.HTTPListen != "" && c.HTTPListen != "off", Home: paths.Home, HTTPListen: c.HTTPListen, DataDir: c.DataDir,
		DBPath: c.DBPath, MediaDir: c.MediaDir, LogLevel: c.LogLevel,
		DashboardDisabled: c.Dashboard.Disabled, DashboardRequireToken: *c.Dashboard.RequireToken,
		NodesEnabled: c.Nodes.Enabled, NodesListen: c.Nodes.Listen,
		TailnetEnabled: c.Core.Tailnet.Enabled, TailnetHostname: c.Core.Tailnet.Hostname,
		LeaseEnabled: c.Core.Lease.Enabled, LeaseRenewSeconds: c.Core.Lease.RenewSeconds, LeaseStaleSeconds: c.Core.Lease.StaleSeconds,
		VaultBucket: c.Vault.Bucket,
	}
}

// Shorthands for paths of Layout.
func (b Boot) TokenPath() string        { return b.Layout().DashboardToken() }
func (b Boot) LockPath() string         { return b.Layout().Lock() }
func (b Boot) BackupDir() string        { return b.Layout().Backup() }
func (b Boot) RcloneConf() string       { return b.Layout().RcloneConf() }
func (b Boot) GoogleClientPath() string { return b.Layout().GoogleClient() }
func (b Boot) PlaudDir() string         { return b.Layout().Plaud() }
func (b Boot) OTPDir() string           { return b.Layout().OTP() }

// EnsureDirs creates the data dir and the folders of the database and the
// media. Resolve does not: it runs for check and open too, which must
// not make a ~/.fridolin for a config file that is not there, and for
// every dashboard save, which would make a folder typed into Settings
// before the restart that uses it. The bridge calls EnsureDirs once, as it
// starts.
func (b Boot) EnsureDirs() error {
	if err := os.MkdirAll(b.DataDir, 0o700); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(b.DBPath), 0o700); err != nil {
		return fmt.Errorf("creating db directory: %w", err)
	}
	if err := os.MkdirAll(b.MediaDir, 0o700); err != nil {
		return fmt.Errorf("creating media directory: %w", err)
	}
	return nil
}
