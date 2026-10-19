package config

import "fmt"

// Field describes one configuration JSON leaf. Boot fields wait for restart;
// dashboard.require_token does so only when protection is turned off.
type Field struct {
	Name string
	Boot bool
}
type fieldSpec struct {
	Field
	get   func(*File) string
	waits func(start, now *File) bool
}

// fields is the single inventory of config leaves, with restart rows first in
// their established reporting order. Collections and custom JSON values are leaves.
var fields = []fieldSpec{
	{Field: Field{Name: "http_listen", Boot: true}, get: func(c *File) string { return c.HTTPListen }},
	{Field: Field{Name: "data_dir", Boot: true}, get: func(c *File) string { return c.DataDir }},
	{Field: Field{Name: "db_path", Boot: true}, get: func(c *File) string { return c.DBPath }},
	{Field: Field{Name: "media_dir", Boot: true}, get: func(c *File) string { return c.MediaDir }},
	{Field: Field{Name: "log_level", Boot: true}, get: func(c *File) string { return c.LogLevel }},
	{Field: Field{Name: "dashboard.disabled", Boot: true}, get: func(c *File) string { return fmt.Sprint(c.Dashboard.Disabled) }},
	{Field: Field{Name: "dashboard.require_token", Boot: true}, waits: func(start, now *File) bool { return start.DashboardRequiresToken() && !now.DashboardRequiresToken() }},
	{Field: Field{Name: "_comment"}},
	{Field: Field{Name: "version"}},
	{Field: Field{Name: "allowed_numbers"}},
	{Field: Field{Name: "allow_any_dm"}},
	{Field: Field{Name: "allowed_groups"}},
	{Field: Field{Name: "group_members"}},
	{Field: Field{Name: "triggers"}},
	{Field: Field{Name: "claude_path"}},
	{Field: Field{Name: "agent_provider"}},
	{Field: Field{Name: "codex_path"}},
	{Field: Field{Name: "codex_model"}},
	{Field: Field{Name: "codex_effort"}},
	{Field: Field{Name: "max_chunk_size"}},
	{Field: Field{Name: "max_media_mb"}},
	{Field: Field{Name: "brain_model"}},
	{Field: Field{Name: "brain_effort"}},
	{Field: Field{Name: "disallowed_tools"}},
	{Field: Field{Name: "dangerously_skip_permissions"}},
	{Field: Field{Name: "run_timeout_minutes"}},
	{Field: Field{Name: "max_concurrent_runs"}},
	{Field: Field{Name: "inherit_claude_user_config"}},
	{Field: Field{Name: "child_env_passthrough"}},
	{Field: Field{Name: "brain_mode"}},
	{Field: Field{Name: "brain_dirs"}},
	{Field: Field{Name: "sandbox.enabled"}},
	{Field: Field{Name: "sandbox.deny_paths"}},
	{Field: Field{Name: "sandbox.allow_paths"}},
	{Field: Field{Name: "sandbox.git_config"}},
	{Field: Field{Name: "sandbox.credentials_dir"}},
	{Field: Field{Name: "otp_guard"}},
	{Field: Field{Name: "otp_markers"}},
	{Field: Field{Name: "viewer"}},
	{Field: Field{Name: "queue.mode"}},
	{Field: Field{Name: "queue.max_pending"}},
	{Field: Field{Name: "queue.notify"}},
	{Field: Field{Name: "debounce.quiet_ms"}},
	{Field: Field{Name: "debounce.max_wait_ms"}},
	{Field: Field{Name: "ingest.enabled"}},
	{Field: Field{Name: "startup_checks.required_files"}},
	{Field: Field{Name: "startup_checks.notify_whatsapp"}},
	{Field: Field{Name: "startup_checks.notify_jid"}},
	{Field: Field{Name: "startup_checks.hint"}},
	{Field: Field{Name: "backup.enabled"}},
	{Field: Field{Name: "backup.remote"}},
	{Field: Field{Name: "backup.root"}},
	{Field: Field{Name: "backup.daily_at"}},
	{Field: Field{Name: "backup.rclone_path"}},
	{Field: Field{Name: "backup.keep_versions_days"}},
	{Field: Field{Name: "backup.include_bridge_config"}},
	{Field: Field{Name: "backup.include_secret_files"}},
	{Field: Field{Name: "plaud.enabled"}},
	{Field: Field{Name: "plaud.brain"}},
	{Field: Field{Name: "plaud.cli_path"}},
	{Field: Field{Name: "plaud.interval_seconds"}},
	{Field: Field{Name: "plaud.polished"}},
	{Field: Field{Name: "plaud.message"}},
	{Field: Field{Name: "plaud.brain_access"}},
	{Field: Field{Name: "plaud.phone_dir"}},
	{Field: Field{Name: "plaud.phone_upload"}},
	{Field: Field{Name: "script_api.tokenless_send"}},
}

// Fields returns an owned copy of the complete configuration field inventory.
func Fields() []Field {
	out := make([]Field, len(fields))
	for i, f := range fields {
		out[i] = f.Field
	}
	return out
}

// RestartFieldNames lists restart-sensitive settings in reporting order.
func RestartFieldNames() []string {
	var out []string
	for _, f := range fields {
		if f.Boot {
			out = append(out, f.Name)
		}
	}
	return out
}

func restartRequired(old, next *File) []string {
	var out []string
	for _, f := range fields {
		if !f.Boot {
			continue
		}
		if f.waits != nil && f.waits(old, next) || f.waits == nil && f.get(old) != f.get(next) {
			out = append(out, f.Name)
		}
	}
	return out
}
