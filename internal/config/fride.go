package config

import (
	"maps"
	"slices"
)

// FrideConfig is how FrIDE's news reaches the chats that handed it work
// (docs/NODES.md, "FrIDE").
type FrideConfig struct {
	// News is FrideNewsQuiet (default): only what is done or merged, what
	// failed, and real questions; or FrideNewsAll: every piece of news.
	News string `json:"news,omitempty"`
}

// The values fride.news takes.
const (
	FrideNewsQuiet = "quiet"
	FrideNewsAll   = "all"
)

// validFrideNews are the values fride.news takes once Resolve() has filled
// in the default.
var validFrideNews = map[string]bool{FrideNewsQuiet: true, FrideNewsAll: true}

// FrideNewsModes returns the values fride.news takes, sorted.
func FrideNewsModes() []string { return slices.Sorted(maps.Keys(validFrideNews)) }
