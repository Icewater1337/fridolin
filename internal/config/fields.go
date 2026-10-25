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
	get   func(*Settings) string
	waits func(start, now *Settings) bool
}

// fields is the single inventory of config leaves, with restart rows first in
// their established reporting order. Collections and custom JSON values are leaves.
var fields = []fieldSpec{
	{Field: Field{Name: "http_listen", Boot: true}, get: func(c *Settings) string { return c.HTTPListen }},
	{Field: Field{Name: "data_dir", Boot: true}, get: func(c *Settings) string { return c.DataDir }},
	{Field: Field{Name: "db_path", Boot: true}, get: func(c *Settings) string { return c.DBPath }},
	{Field: Field{Name: "media_dir", Boot: true}, get: func(c *Settings) string { return c.MediaDir }},
	{Field: Field{Name: "log_level", Boot: true}, get: func(c *Settings) string { return c.LogLevel }},
	{Field: Field{Name: "dashboard.disabled", Boot: true}, get: func(c *Settings) string { return fmt.Sprint(c.Dashboard.Disabled) }},
	{Field: Field{Name: "dashboard.require_token", Boot: true}, waits: func(start, now *Settings) bool { return start.Dashboard.RequireToken && !now.Dashboard.RequireToken }},
	// The node port opens at startup (internal/nodes), on the address it
	// had then.
	{Field: Field{Name: "nodes.enabled", Boot: true}, get: func(c *Settings) string { return fmt.Sprint(c.Nodes.Enabled) }},
	{Field: Field{Name: "nodes.listen", Boot: true}, get: func(c *Settings) string { return c.Nodes.Listen }},
	// The tailnet node joins, and the core lease is taken, as the bridge
	// starts (internal/tailnet, internal/lease), with these values.
	{Field: Field{Name: "core.tailnet.enabled", Boot: true}, get: func(c *Settings) string { return fmt.Sprint(c.Core.Tailnet.Enabled) }},
	{Field: Field{Name: "core.tailnet.hostname", Boot: true}, get: func(c *Settings) string { return c.Core.Tailnet.Hostname }},
	{Field: Field{Name: "core.lease.enabled", Boot: true}, get: func(c *Settings) string { return fmt.Sprint(c.Core.Lease.Enabled) }},
	{Field: Field{Name: "core.lease.renew_seconds", Boot: true}, get: func(c *Settings) string { return fmt.Sprint(c.Core.Lease.RenewSeconds) }},
	{Field: Field{Name: "core.lease.stale_seconds", Boot: true}, get: func(c *Settings) string { return fmt.Sprint(c.Core.Lease.StaleSeconds) }},
	{Field: Field{Name: "vault.bucket", Boot: true}, get: func(c *Settings) string { return c.Vault.Bucket }},
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
	{Field: Field{Name: "timezone"}},
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
	{Field: Field{Name: "plaud.voice"}},
	{Field: Field{Name: "plaud.voice_brain"}},
	{Field: Field{Name: "plaud.voice_quiet"}},
	{Field: Field{Name: "script_api.tokenless_send"}},
	{Field: Field{Name: "core.memory_limit_gb"}},
	{Field: Field{Name: "vault.enabled"}},
	{Field: Field{Name: "vault.hot_budget_gb"}},
	{Field: Field{Name: "vault.activity_days"}},
	{Field: Field{Name: "vault.cold_after_days"}},
	{Field: Field{Name: "vault.headroom_percent"}},
	{Field: Field{Name: "vault.snapshots_bucket"}},
	{Field: Field{Name: "vault.endpoint"}},
	{Field: Field{Name: "vault.b2_daily_budget"}},
	{Field: Field{Name: "vault.targets"}},
	{Field: Field{Name: "vault.secret_dirs"}},
	{Field: Field{Name: "vault.secret_keep_days"}},
	{Field: Field{Name: "policy.max_runs_per_hour"}},
	{Field: Field{Name: "policy.max_pending_irreversible"}},
	{Field: Field{Name: "policy.approval_timeout_hours"}},
	{Field: Field{Name: "housekeeping.enabled"}},
	{Field: Field{Name: "housekeeping.every_hours"}},
	{Field: Field{Name: "housekeeping.dry_run"}},
	{Field: Field{Name: "housekeeping.cpu_percent"}},
	{Field: Field{Name: "housekeeping.rss_mb"}},
	{Field: Field{Name: "housekeeping.min_age_minutes"}},
	{Field: Field{Name: "schedules.enabled"}},
	{Field: Field{Name: "schedules.jitter_minutes"}},
	{Field: Field{Name: "schedules.keep_days"}},
	{Field: Field{Name: "schedules.keep_runs"}},
	{Field: Field{Name: "schedules.max_total_mb"}},
	{Field: Field{Name: "schedules.max_log_kb"}},
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

func restartRequired(old, next *Settings) []string {
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
