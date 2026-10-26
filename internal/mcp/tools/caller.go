// Package tools is the plumbing of the bridge's MCP tools, which a run
// reaches through fridolin mcp: the Caller a call comes from (the run's
// brain and what its manifest grants), the Set of tools with the Guard
// that tools which act on the world go through and the audit line each
// call writes, and the helpers that describe a tool's input. The tools
// themselves stay with their features; internal/app registers them and
// supplies the Guard and the audit log.
package tools

import (
	"strings"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

// Caller is the run a tool call comes from, as the bridge knows it: the
// run's token names the run, and the brain's approved manifest says what
// its grants are now. Nothing in it comes from the model.
type Caller struct {
	// Brain is the run's brain.
	Brain string
	// Delegated is a run that does a task the core brain delegated. It
	// reaches no other brain, so delegation goes one level deep.
	Delegated bool
	// Manifest is the brain's approved manifest as the call came in.
	Manifest manifest.Manifest
	// Running reports whether a brain is loaded and can take work.
	Running func(id string) bool
}

// Core reports whether the run is the core brain's own, not a task
// delegated to it: only then may it reach other brains.
func (c Caller) Core() bool { return c.Manifest.IsCore() && !c.Delegated }

// Granted lists the brains the run may use for kind, sorted: none unless
// it is a core run, and only brains that are loaded.
func (c Caller) Granted(kind string) []string {
	if !c.Core() {
		return nil
	}
	var out []string
	for _, id := range c.Manifest.GrantedTo(kind) {
		if c.Running(id) {
			out = append(out, id)
		}
	}
	return out
}

// MayRead reports whether the run may read brain id's memories and skills:
// its own always, another's with a read grant.
func (c Caller) MayRead(id string) bool {
	return id == c.Brain || c.Core() && c.Manifest.Granted(id, manifest.GrantRead) && c.Running(id)
}

// BrainList names brains with their display names: "family (Family)".
// name gives a brain's display name, "" when it has none.
func BrainList(ids []string, name func(string) string) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = id
		if n := name(id); n != "" && n != id {
			parts[i] += " (" + n + ")"
		}
	}
	return strings.Join(parts, ", ")
}
