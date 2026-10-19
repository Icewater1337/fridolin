package main

import (
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// observeRuns reports whether m, untagged group chatter, gets a run of its
// own in a brain that observes with mode. Junk (a bare emoji, "ok") never
// does, but stays in the history like the rest: a "yes" may answer what
// the brain asked.
func observeRuns(mode string, m brain.Inbound) bool {
	return mode == manifest.ObserveRun && (len(m.Media) > 0 || !IsJunk(m.Text))
}
