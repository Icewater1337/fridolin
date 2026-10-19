package config

import "path/filepath"

// Boot holds values captured once when the configuration store opens. It is a
// value copy: live saves keep their requested values in Config for the API and
// next restart, while running components use these paths and startup flags.
type Boot struct {
	Home                  string `config:"-"`
	HTTPListen            string `config:"http_listen"`
	DataDir               string `config:"data_dir"`
	DBPath                string `config:"db_path"`
	MediaDir              string `config:"media_dir"`
	LogLevel              string `config:"log_level"`
	DashboardDisabled     bool   `config:"dashboard.disabled"`
	DashboardRequireToken bool   `config:"dashboard.require_token"`
}

func bootOf(c *Config, paths Paths) Boot {
	return Boot{
		Home: paths.Home, HTTPListen: c.HTTPListen, DataDir: c.DataDir,
		DBPath: c.DBPath, MediaDir: c.MediaDir, LogLevel: c.LogLevel,
		DashboardDisabled: c.Dashboard.Disabled, DashboardRequireToken: c.DashboardRequiresToken(),
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
