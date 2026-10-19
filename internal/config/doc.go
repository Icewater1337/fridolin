// Package config owns the persisted File schema, validation, defaults,
// migrations and atomic configuration transactions. Resolve derives immutable
// Settings with startup resources in Boot and resolved policy values in Live.
// EffectiveFile preserves the dashboard's JSON representation. Paths, platform
// and environment access are explicit inputs; resolution creates no directories.
package config
