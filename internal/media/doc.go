// Package media owns the attachment inbox: bounded downloads, unique file
// names, per-brain ownership, confined ingestion and legacy-folder migration.
// It accepts directory and lifecycle settings from its caller and knows
// nothing of Config, WhatsApp, HTTP responses, or the process environment.
package media
