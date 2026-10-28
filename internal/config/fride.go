package config

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// FrideConfig is how FrIDE's news reaches the chats that handed it work
// (docs/NODES.md, "FrIDE").
type FrideConfig struct {
	// News is FrideNewsQuiet (default): only what is done or merged, what
	// failed, and real questions; or FrideNewsAll: every piece of news.
	News string `json:"news,omitempty"`
	// Instances are the FrIDE instances the core brain may start on its
	// nodes, by name: each a whole FrIDE in a Docker Sandbox of its own,
	// for one trust domain (docs/NODES.md, "FrIDE instances").
	Instances map[string]FrideInstanceConfig `json:"instances,omitempty"`
}

// FrideInstanceConfig is one FrIDE instance.
type FrideInstanceConfig struct {
	// Description tells the core brain what the instance is for.
	Description string `json:"description,omitempty"`
	// Repos are git URLs, cloned inside the instance when missing.
	Repos []string `json:"repos,omitempty"`
	// Identities name the git identities it gets (github, gitlab): only
	// these reach it, as placeholders for their tokens.
	Identities []string `json:"identities,omitempty"`
	// Egress are hosts it may reach beyond its baseline.
	Egress []string `json:"egress,omitempty"`
	// Nodes are where it may run, by id or name; empty: any approved
	// node that offers fride_host.
	Nodes []string `json:"nodes,omitempty"`
	// MemoryMB is a fixed size; 0 lets the core decide at each start.
	MemoryMB int `json:"memory_mb,omitempty"`
	// IdleStopMinutes stops it after this long with no open thread and
	// no news: 0 takes FrideIdleStopDefault, -1 never stops it.
	IdleStopMinutes int `json:"idle_stop_minutes,omitempty"`
}

// FrideIdleStopDefault is how long an instance idles before it stops,
// in minutes, when its config does not say.
const FrideIdleStopDefault = 30

// IdleStop is how long the instance idles before it stops, in minutes;
// 0 means never.
func (f FrideInstanceConfig) IdleStop() int {
	switch {
	case f.IdleStopMinutes < 0:
		return 0
	case f.IdleStopMinutes == 0:
		return FrideIdleStopDefault
	}
	return f.IdleStopMinutes
}

// frideInstanceName is what an instance may be called (nodeproto's
// CheckFrideInstanceName, which this package does not import).
var frideInstanceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)

// The identities an instance may name: the git identities the core holds
// (internal/gitident).
var frideIdentities = []string{"github", "gitlab"}

// Bounds of fride.instances.
const (
	maxFrideInstances      = 16
	maxFrideInstanceList   = 64
	maxFrideInstanceMemory = 65536
	minFrideInstanceMemory = 2048
)

// validateFrideInstances reports what is wrong with fride.instances.
func validateFrideInstances(in map[string]FrideInstanceConfig) error {
	if len(in) > maxFrideInstances {
		return fmt.Errorf("fride.instances: at most %d", maxFrideInstances)
	}
	for _, name := range slices.Sorted(maps.Keys(in)) {
		f := in[name]
		if !frideInstanceName.MatchString(name) || name == "native" {
			return fmt.Errorf("fride.instances: %q is no instance name: a lower-case letter, then up to 23 lower-case letters, digits and dashes, and not \"native\"", name)
		}
		if len(f.Repos) > maxFrideInstanceList || len(f.Egress) > maxFrideInstanceList || len(f.Nodes) > maxFrideInstanceList {
			return fmt.Errorf("fride.instances.%s: at most %d repos, egress hosts and nodes each", name, maxFrideInstanceList)
		}
		for _, r := range f.Repos {
			if r == "" || strings.ContainsAny(r, " \t\r\n") {
				return fmt.Errorf("fride.instances.%s.repos: %q is no git URL", name, r)
			}
		}
		for _, id := range f.Identities {
			if !slices.Contains(frideIdentities, id) {
				return fmt.Errorf("fride.instances.%s.identities: %q is not one of %s", name, id, strings.Join(frideIdentities, ", "))
			}
		}
		for _, h := range f.Egress {
			if h == "" || strings.ContainsAny(h, " \t\r\n/:") {
				return fmt.Errorf("fride.instances.%s.egress: %q is no host name", name, h)
			}
		}
		if f.MemoryMB != 0 && (f.MemoryMB < minFrideInstanceMemory || f.MemoryMB > maxFrideInstanceMemory) {
			return fmt.Errorf("fride.instances.%s.memory_mb: 0 (the core decides) or from %d to %d", name, minFrideInstanceMemory, maxFrideInstanceMemory)
		}
		if f.IdleStopMinutes < -1 || f.IdleStopMinutes > 1440 {
			return fmt.Errorf("fride.instances.%s.idle_stop_minutes: -1 (never), 0 (%d) or up to 1440", name, FrideIdleStopDefault)
		}
	}
	return nil
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
