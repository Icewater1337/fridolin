package main

import (
	"context"
	"io"

	"github.com/Icewater1337/fridolin/internal/sandbox"
)

// SiteOptions provides startup paths and callbacks owned by this instance.
// GoogleClient and Builder callbacks remain live: the manager calls them at
// the operation boundaries where the original bridge hooks were read.
type SiteOptions struct {
	DataDir      string
	GoogleClient func() string
	Builder      SiteBuilderHooks
	// Nil installs use the original Node/npm and sandboxed workspace runners.
	NPMInstall       func(context.Context, string, bool) error
	WorkspaceInstall func(context.Context, string, string) error
	// Empty uses the platform's normal sandbox-exec path.
	SandboxExec string
}

// SiteBuilderHooks connect the site to the brains. Supplied at construction.
type SiteBuilderHooks struct {
	// BrainDir returns a brain's folder, or "" for no such brain.
	BrainDir func(id string) string
	// Sandbox wraps bin+args to run under the brain's sandbox profile (or
	// returns them unchanged when sandboxing is off), with the variables
	// that point the command at the brain's own TMPDIR and caches, the only
	// places outside its folder it may write.
	Sandbox func(brainID, bin string, args []string) (string, []string, []string, error)
	// BridgeURL is the bridge's own HTTP address, for the MCP server.
	BridgeURL func() string
	// Executable is the fridolin binary the MCP server runs from.
	Executable func() (string, error)
	// SiteLimits is what the live site's sandbox keeps from module code
	// beyond its own list: the bridge's files (SandboxProfiler.sitePaths)
	// and its HTTP port.
	SiteLimits func() sandbox.SiteLimits
	// Ingest hands files (already in the media folder) to a brain, as
	// /ingest does; MediaDir is that folder (site_ingest.go).
	Ingest   func(brainID, sender, text string, files []string) error
	MediaDir func() string
	// Search runs a read-only Claude agent in a brain's folder, its
	// stream-json output to out (site_ingest.go).
	Search func(ctx context.Context, brainID, prompt string, out io.Writer) error
	// Device answers a phone's request the site passed on, which the
	// bridge checks itself (plaud_app.go, site_ingest.go).
	Device func(op, assertion string, body []byte) (map[string]any, error)
}
