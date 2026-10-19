// Package sandbox builds the brain and live-site sandbox-exec profiles.
// Callers supply host paths, resolved programs and installation layout;
// profile construction never reads ambient process settings. Filesystem
// inspection preserves the existing symlink, pinning and rule precedence
// contracts. The runtime owns locking, discovery and fail-closed execution.
package sandbox
