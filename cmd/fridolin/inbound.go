package main

import (
	"slices"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
)

// Inbound carries message content plus the brain's history and queue bookkeeping.
type Inbound struct {
	chat.Inbound
	// logged is the line Brain.Submit wrote to the history for the
	// message; a merged message's parts each keep their own. Its prompt
	// shows the chat's history before the first of them, and what the
	// brain sent there after it (see Brain.historyBefore).
	logged history.Entry
	// parts are the messages this one was merged from, oldest first, when
	// there were several (see merge), so that an edit or a deletion of one
	// of them can redo the merge (see applyChange). Empty for one message.
	parts []Inbound
}

// merge folds a later message into m. The batch is tagged if any part was:
// a file followed by "@claude what is this?" must get an answer.
func (m Inbound) Merge(next Inbound) Inbound {
	parts := append(m.ownParts(), next.ownParts()...)
	m.Inbound = m.Inbound.Merge(next.Inbound)
	m.parts = parts
	return m
}

// ownParts returns the messages m is made of: its parts, or m itself.
func (m Inbound) ownParts() []Inbound {
	if len(m.parts) > 0 {
		return append([]Inbound(nil), m.parts...)
	}
	m.parts = nil
	return []Inbound{m}
}

// withMediaMoved returns m with each file in moved (see claimMedia) named
// by its new path, in m.Media and in the Media of each of its parts: the
// history has a line per part, and an edit or a deletion of one part
// merges the others again from theirs (see applyChange). With only
// m.Media changed, those named where the files had been.
func (m Inbound) withMediaMoved(moved map[string]string) Inbound {
	if len(moved) == 0 {
		return m
	}
	m.Media = movedPaths(m.Media, moved)
	if len(m.parts) > 0 {
		parts := make([]Inbound, len(m.parts))
		for i, p := range m.parts {
			p.Media = movedPaths(p.Media, moved)
			parts[i] = p
		}
		m.parts = parts
	}
	return m
}

// changes reports whether c changes one of m's parts: the message c
// names, in the same chat, from the same sender. A client can put any ID
// in an edit or a deletion, and one group member must not rewrite or
// cancel what another asked for; so a group admin's deletion of someone
// else's message changes nothing here either.
func (m Inbound) Changes(c chat.Change) bool {
	return slices.ContainsFunc(m.ownParts(), func(p Inbound) bool { return c.Names(p.Inbound) })
}

// remerge merges parts again, as the debouncer did, into one message.
func remerge(parts []Inbound) Inbound {
	m := parts[0]
	for _, p := range parts[1:] {
		m = m.Merge(p)
	}
	return m
}

// applyChange applies c to m: the part c names is deleted, or gets its
// new text. It reports whether m holds that part, and keep is false when
// nothing is left of m.
func (m Inbound) ApplyChange(c chat.Change) (out Inbound, found, keep bool) {
	parts := m.ownParts()
	i := slices.IndexFunc(parts, func(p Inbound) bool { return c.Names(p.Inbound) })
	if i < 0 {
		return m, false, true
	}
	switch c.Kind {
	case chat.ChangeRevoke:
		parts = append(parts[:i], parts[i+1:]...)
	case chat.ChangeEdit:
		parts[i].Text = c.Text
	}
	if len(parts) == 0 {
		return Inbound{}, true, false
	}
	return remerge(parts), true, true
}

// movedPaths returns a copy of paths with each one in moved replaced by
// where it went.
func movedPaths(paths []string, moved map[string]string) []string {
	out := slices.Clone(paths)
	for i, p := range out {
		if to, ok := moved[p]; ok {
			out[i] = to
		}
	}
	return out
}
