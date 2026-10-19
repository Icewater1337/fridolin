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
}

func bootOf(c *File, paths Paths) Boot {
	return Boot{
		HTTPEnabled: c.HTTPListen != "" && c.HTTPListen != "off", Home: paths.Home, HTTPListen: c.HTTPListen, DataDir: c.DataDir,
		DBPath: c.DBPath, MediaDir: c.MediaDir, LogLevel: c.LogLevel,
		DashboardDisabled: c.Dashboard.Disabled, DashboardRequireToken: *c.Dashboard.RequireToken,
	}
}

func (b Boot) TokenPath() string  { return filepath.Join(b.DataDir, "dashboard.token") }
func (b Boot) LockPath() string   { return filepath.Join(b.DataDir, "bridge.lock") }
func (b Boot) BackupDir() string  { return filepath.Join(b.DataDir, "backup") }
func (b Boot) RcloneConf() string { return filepath.Join(b.DataDir, "rclone", "rclone.conf") }
func (b Boot) GoogleClientPath() string {
	return filepath.Join(b.DataDir, "google", "oauth_client.json")
}
func (b Boot) PlaudDir() string { return filepath.Join(b.DataDir, "plaud") }
func (b Boot) OTPDir() string   { return filepath.Join(b.DataDir, "otp") }

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
