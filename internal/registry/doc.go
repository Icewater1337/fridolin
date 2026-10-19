// Package registry prepares brain folders from caller-supplied templates.
// Its scaffold transaction retains directory handles and ownership records so
// a failed registration removes only unchanged files it created. Configuration
// registration and runtime reconciliation still belong to the caller.
package registry
