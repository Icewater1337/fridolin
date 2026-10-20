package route

import (
	"fmt"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// Validate checks brains against each other: unique ids, every group
// owned by one brain, at most one DM brain and one default, no two brains in
// one folder and no brain folder inside another (sandbox profiles deny other
// brains' folders, so a nested brain could not read itself), and at most
// one core brain.
func Validate(specs []manifest.Spec) error {
	ids := map[string]string{}
	groups := map[string]string{}
	dm, def, core := "", "", ""
	for _, s := range specs {
		m := s.Manifest
		if other, ok := ids[m.ID]; ok {
			return fmt.Errorf("two brains use the id %q (%s and %s)", m.ID, other, s.Dir)
		}
		ids[m.ID] = s.Dir
		if m.IsCore() {
			if core != "" {
				return fmt.Errorf("brains %q and %q are both the core brain (role %q), and there is only one", core, m.ID, manifest.RoleCore)
			}
			core = m.ID
		}
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

// Held is the approved routing of a brain that cannot currently run. The
// caller decides which resolution failures reserve their routes.
type Held struct {
	ID      string
	Routing manifest.Routing
}

// Table is a snapshot of runnable brains and the routes reserved for brains
// temporarily left out. It owns its routing values independently of callers.
type Table struct {
	entries             map[string]routeEntry
	groups              map[string]string
	dm                  string
	fallback            string
	dirs                []string
	heldDM, heldDefault string
	heldGroups          map[string]string
}

type routeEntry struct {
	ReplyChat  string
	groups     []string
	handlesDMs bool
}

// Build validates the set and records its current routing values.
// Individual manifest validation belongs to the caller resolving the specs.
func Build(specs []manifest.Spec) (*Table, error) {
	return BuildWithHeld(specs, nil)
}

// BuildWithHeld keeps unavailable brains' approved routes from falling
// through to an unrelated brain. Active explicit routes take precedence;
// the first held claim wins when several unavailable brains claim a route.
func BuildWithHeld(specs []manifest.Spec, held []Held) (*Table, error) {
	if err := Validate(specs); err != nil {
		return nil, err
	}
	t := &Table{entries: make(map[string]routeEntry, len(specs)), groups: map[string]string{}}
	for _, s := range specs {
		m := s.Manifest
		t.entries[m.ID] = routeEntry{ReplyChat: m.ReplyChat, groups: append([]string(nil), m.Routing.Groups...), handlesDMs: m.Routing.HandlesDMs}
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
	t.heldGroups = map[string]string{}
	for _, h := range held {
		if h.Routing.HandlesDMs && t.dm == "" && t.heldDM == "" {
			t.heldDM = h.ID
		}
		for _, group := range h.Routing.Groups {
			if t.groups[group] == "" && t.heldGroups[group] == "" {
				t.heldGroups[group] = h.ID
			}
		}
		if h.Routing.Default && t.heldDefault == "" {
			t.heldDefault = h.ID
		}
	}
	if t.fallback == "" && len(specs) > 0 && t.heldDefault == "" {
		t.fallback = specs[0].Manifest.ID
	}
	return t, nil
}

// For returns the brain id that handles the chat, or empty for an empty set.
func (t *Table) For(jid string, isGroup bool) string {
	if t == nil {
		return ""
	}
	if isGroup {
		if id, ok := t.groups[jid]; ok {
			return id
		}
		if t.heldGroups[jid] != "" {
			return ""
		}
	} else if t.dm != "" {
		return t.dm
	} else if t.heldDM != "" {
		return ""
	}
	return t.fallback
}

// HeldFor returns the unavailable brain whose route prevents fallback, or
// empty when a runnable brain (or no configured brain) takes the chat.
func (t *Table) HeldFor(jid string, isGroup bool) string {
	if t == nil {
		return ""
	}
	if isGroup {
		if t.groups[jid] != "" {
			return ""
		}
		if id := t.heldGroups[jid]; id != "" {
			return id
		}
	} else {
		if t.dm != "" {
			return ""
		}
		if t.heldDM != "" {
			return t.heldDM
		}
	}
	if t.fallback == "" {
		return t.heldDefault
	}
	return ""
}

// Has reports whether id belongs to this table.
func (t *Table) Has(id string) bool { _, ok := t.entries[id]; return ok }

// Default returns the explicit default brain, or the first configured brain.
func (t *Table) Default() string {
	if t == nil {
		return ""
	}
	return t.fallback
}

// Dirs returns the canonical folders in configured order as a fresh slice.
func (t *Table) Dirs() []string { return append([]string(nil), t.dirs...) }

// ReplyChat selects the reply destination for a brain in this table.
func (t *Table) ReplyChat(id, ownerDM string) string {
	e, ok := t.entries[id]
	if !ok {
		return ""
	}
	m := manifest.Manifest{ReplyChat: e.ReplyChat, Routing: manifest.Routing{HandlesDMs: e.handlesDMs, Groups: e.groups}}
	return ReplyChat(m, e.handlesDMs || t.For("", false) == id, ownerDM)
}

// ReplyChat preserves the configured reply, then DM-before-group ordering.
// The runtime caller supplies getsDMs from its current pointer/held-route state.
func ReplyChat(m manifest.Manifest, getsDMs bool, ownerDM string) string {
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
