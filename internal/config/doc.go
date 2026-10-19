// Package config owns persisted configuration values, validation, defaults and
// migrations. Resolve preserves the current file shape and path derivation;
// loading and transaction orchestration remain in the command until the next
// extraction slice. Configuration resolution writes no directories.
package config
