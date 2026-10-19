package main

import (
	"fmt"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// ValidateBrainSet checks brains against each other: unique ids, every group
// owned by one brain, at most one DM brain and one default, no two brains in
// one folder and no brain folder inside another (sandbox profiles deny other
// brains' folders, so a nested brain could not read itself).
func ValidateBrainSet(specs []manifest.Spec) error {
	ids := map[string]string{}
	groups := map[string]string{}
	dm, def := "", ""
	for _, s := range specs {
		m := s.Manifest
		if other, ok := ids[m.ID]; ok {
			return fmt.Errorf("two brains use the id %q (%s and %s)", m.ID, other, s.Dir)
		}
		ids[m.ID] = s.Dir
		for _, g := range m.Routing.Groups {
			if owner, ok := groups[g]; ok {
				return fmt.Errorf("group %s is claimed by both %q and %q", g, owner, m.ID)
			}
			groups[g] = m.ID
		}
		if m.Routing.HandlesDMs {
			if dm != "" {
				return fmt.Errorf("brains %q and %q both handle DMs", dm, m.ID)
			}
			dm = m.ID
		}
		if m.Routing.Default {
			if def != "" {
				return fmt.Errorf("brains %q and %q are both marked default", def, m.ID)
			}
			def = m.ID
		}
	}
	for i, a := range specs {
		for j, b := range specs {
			switch {
			case i == j:
			case a.Dir == b.Dir:
				return fmt.Errorf("brains %q and %q use the same folder %s", a.Manifest.ID, b.Manifest.ID, a.Dir)
			case fsx.Within(b.Dir, a.Dir):
				return fmt.Errorf("brain folder %s is inside brain folder %s", b.Dir, a.Dir)
			}
		}
	}
	return nil
}

// routeTable is a snapshot of routing from approved, runnable brain specs.
// Runtime routes held for unavailable brains remain with the manager until
// its later extraction; this table does not replace that runtime state.
type routeTable struct {
	entries  map[string]routeEntry
	groups   map[string]string
	dm       string
	fallback string
	dirs     []string
}

type routeEntry struct {
	replyChat  string
	groups     []string
	handlesDMs bool
}

// buildRouteTable validates the set and records its current routing values.
// Individual manifest validation belongs to the caller resolving the specs.
func buildRouteTable(specs []manifest.Spec) (*routeTable, error) {
	if err := ValidateBrainSet(specs); err != nil {
		return nil, err
	}
	t := &routeTable{entries: make(map[string]routeEntry, len(specs)), groups: map[string]string{}}
	for _, s := range specs {
		m := s.Manifest
		t.entries[m.ID] = routeEntry{replyChat: m.ReplyChat, groups: append([]string(nil), m.Routing.Groups...), handlesDMs: m.Routing.HandlesDMs}
		t.dirs = append(t.dirs, s.Dir)
		for _, group := range m.Routing.Groups {
			t.groups[group] = m.ID
		}
		if m.Routing.HandlesDMs {
			t.dm = m.ID
		}
		if m.Routing.Default {
			t.fallback = m.ID
		}
	}
	if t.fallback == "" && len(specs) > 0 {
		t.fallback = specs[0].Manifest.ID
	}
	return t, nil
}

// For returns the brain id that handles the chat, or empty for an empty set.
func (t *routeTable) For(jid string, isGroup bool) string {
	if isGroup {
		if id, ok := t.groups[jid]; ok {
			return id
		}
	} else if t.dm != "" {
		return t.dm
	}
	return t.fallback
}

// Has reports whether id belongs to this table.
func (t *routeTable) Has(id string) bool { _, ok := t.entries[id]; return ok }

// Default returns the explicit default brain, or the first configured brain.
func (t *routeTable) Default() string { return t.fallback }

// Dirs returns the canonical folders in configured order as a fresh slice.
func (t *routeTable) Dirs() []string { return append([]string(nil), t.dirs...) }

// ReplyChat selects the reply destination for a brain in this table.
func (t *routeTable) ReplyChat(id, ownerDM string) string {
	e, ok := t.entries[id]
	if !ok {
		return ""
	}
	m := manifest.Manifest{ReplyChat: e.replyChat, Routing: manifest.Routing{HandlesDMs: e.handlesDMs, Groups: e.groups}}
	return replyChat(m, e.handlesDMs || t.For("", false) == id, ownerDM)
}

// replyChat preserves the configured reply, then DM-before-group ordering.
// The runtime caller supplies getsDMs from its current pointer/held-route state.
func replyChat(m manifest.Manifest, getsDMs bool, ownerDM string) string {
	if m.ReplyChat != "" {
		return m.ReplyChat
	}
	if getsDMs && ownerDM != "" {
		return ownerDM
	}
	if len(m.Routing.Groups) > 0 {
		return m.Routing.Groups[0]
	}
	return ""
}
