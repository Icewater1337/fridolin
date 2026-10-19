// Package app composes configuration, runtimes, persistence, provider adapters
// and HTTP handlers. New constructs an idle application; Run binds its listener,
// resumes durable work and owns shutdown. Restart is an error returned to the
// CLI, which alone replaces the process.
package app
