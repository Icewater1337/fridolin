package main

import (
	"context"
	"slices"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// groupsTTL is how long the list of the groups the account is in is kept.
// The dashboard asks for it every minute and after each change on the
// Chats page, and each answer used to be a query to WhatsApp, which an
// unofficial client had better keep rare. The events that change the
// list drop it at once (see observe), and so does the Chats page's Refresh
// button (ForgetGroups), so the TTL only bounds how long a change that
// came without an event stays unseen when nobody presses it.
const groupsTTL = 10 * time.Minute

// groupsFetchTimeout bounds one query for the list. It runs apart from the
// request that started it, since others may be waiting for it too.
const groupsFetchTimeout = 20 * time.Second

// groupCache keeps the groups the account is in, so the dashboard's polls
// are not each a query to WhatsApp, and requests that come while a query
// runs wait for it instead of sending their own. The zero value is ready.
type groupCache struct {
	mu     sync.Mutex
	groups []Group     // nil when not known
	at     time.Time   // when groups came
	gen    uint64      // counts invalidations
	flight *groupFetch // the query running, if any

	names groupNameCache // the groups' names, which outlast the list
}

// groupFetch is one query for the list; done is closed once groups and
// err hold its answer.
type groupFetch struct {
	gen    uint64 // the generation it was started in
	done   chan struct{}
	groups []Group
	err    error
}

// get returns the list, from fetch when it is not known or older than
// groupsTTL. A failed fetch is not kept, so the next call asks again.
func (c *groupCache) get(ctx context.Context, fetch func(context.Context) ([]Group, error)) ([]Group, error) {
	c.mu.Lock()
	if c.groups != nil && time.Since(c.at) < groupsTTL {
		groups := slices.Clone(c.groups)
		c.mu.Unlock()
		return groups, nil
	}
	f := c.flight
	if f == nil || f.gen != c.gen {
		f = &groupFetch{gen: c.gen, done: make(chan struct{})}
		c.flight = f
		go c.run(context.WithoutCancel(ctx), f, fetch)
	}
	c.mu.Unlock()
	select {
	case <-f.done:
		return slices.Clone(f.groups), f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *groupCache) run(ctx context.Context, f *groupFetch, fetch func(context.Context) ([]Group, error)) {
	ctx, cancel := context.WithTimeout(ctx, groupsFetchTimeout)
	defer cancel()
	groups, err := fetch(ctx)
	if err == nil && groups == nil {
		groups = []Group{} // known to be none
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	f.groups, f.err = groups, err
	if c.flight == f {
		c.flight = nil
	}
	// A list asked for before an invalidation may predate the change: it
	// answers who waited for it, but is not kept.
	if err == nil && f.gen == c.gen {
		c.groups, c.at = groups, time.Now()
		for _, g := range groups {
			c.names.set(g.JID, g.Name)
		}
	}
	close(f.done)
}

// invalidate drops the list, so the next get asks WhatsApp again.
func (c *groupCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.groups = nil
	c.gen++
}

// observe drops the list on the events that may change it: the account
// joined or was added to a group; a group's name, members or anything
// else changed, which is also how leaving one or being removed arrives;
// a new connection, as events may have been missed while it was down;
// and a pairing or a logout, after which it may be another account. The
// name a group joined or was renamed to is kept at once.
func (c *groupCache) observe(evt any) {
	switch evt := evt.(type) {
	case *events.JoinedGroup:
		c.names.set(evt.JID.String(), evt.Name)
	case *events.GroupInfo:
		if evt.Name != nil {
			c.names.set(evt.JID.String(), evt.Name.Name)
		}
	}
	switch evt.(type) {
	case *events.JoinedGroup, *events.GroupInfo, *events.Connected, *events.PairSuccess, *events.LoggedOut:
		c.invalidate()
	}
}

// ForgetGroups drops the list the client keeps, so the next JoinedGroups
// asks WhatsApp: for the dashboard's Refresh button, which should also
// show a change that came without an event.
func (wa *WhatsAppClient) ForgetGroups() { wa.groups.invalidate() }

// groupLister is what JoinedGroups and groupName need from
// whatsmeow.Client; tests swap in a fake to count what reaches WhatsApp.
type groupLister interface {
	IsLoggedIn() bool
	GetJoinedGroups(ctx context.Context) ([]*types.GroupInfo, error)
	GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error)
}

// groupsFrom is what JoinedGroups and groupName ask: the client, read anew
// each time since a logout replaces it, unless a test set lister.
func (wa *WhatsAppClient) groupsFrom() groupLister {
	if wa.lister != nil {
		return wa.lister
	}
	return wa.cl()
}

// The names of groups, for the log and the dashboard's offer of a group
// that wrote without being allowed. whatsmeow asks WhatsApp for a group's
// info each time it is asked, and anyone who adds the bridge's account to
// a group could make it ask once per message; so names come from the
// group list when it is loaded and from the events that rename a group,
// and a group the bridge knows no name for is looked up at most once per
// groupNameTTL, or per groupNameFailTTL after a lookup failed.
const (
	groupNameTTL     = time.Hour
	groupNameFailTTL = 5 * time.Minute
	// groupNameTimeout bounds one lookup, whatever whatsmeow's own
	// timeout (75 seconds) is.
	groupNameTimeout = 5 * time.Second
)

// groupNameCache keeps the names of groups by JID. The zero value is
// ready.
type groupNameCache struct {
	mu      sync.Mutex
	names   map[string]groupNameEntry
	flights map[string]*groupNameFetch // the lookups running, by JID
	gen     uint64                     // counts the names written
}

// groupNameEntry is a group's name, or "" for one a lookup did not find,
// until it is to be looked up again.
type groupNameEntry struct {
	name  string
	until time.Time
	gen   uint64 // the generation it was written in
}

// groupNameFetch is one lookup; done is closed once name holds its answer.
type groupNameFetch struct {
	gen  uint64 // the generation it was started in
	done chan struct{}
	name string
}

// fresh returns jid's entry while it lasts. The caller holds mu.
func (c *groupNameCache) fresh(jid string) (groupNameEntry, bool) {
	e, ok := c.names[jid]
	if !ok || !time.Now().Before(e.until) {
		return groupNameEntry{}, false
	}
	return e, true
}

// cached returns the name kept for jid, "" when none is; it never asks
// WhatsApp.
func (c *groupNameCache) cached(jid string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, _ := c.fresh(jid)
	return e.name
}

// lookup returns jid's name: the one kept, or else what fetch answers
// within groupNameTimeout, under ctx. A lookup that runs is waited for
// rather than repeated. A failure is kept for groupNameFailTTL as "", but
// not one because ctx ended: the client is shutting down.
func (c *groupNameCache) lookup(ctx context.Context, jid string, fetch func(context.Context) (string, error)) string {
	c.mu.Lock()
	if e, ok := c.fresh(jid); ok {
		c.mu.Unlock()
		return e.name
	}
	if f := c.flights[jid]; f != nil {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.name
		case <-ctx.Done():
			return ""
		}
	}
	f := &groupNameFetch{gen: c.gen, done: make(chan struct{})}
	if c.flights == nil {
		c.flights = map[string]*groupNameFetch{}
	}
	c.flights[jid] = f
	c.mu.Unlock()

	fctx, cancel := context.WithTimeout(ctx, groupNameTimeout)
	name, err := fetch(fctx)
	cancel()

	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.flights, jid)
	f.name = name
	switch e, ok := c.names[jid]; {
	case ok && e.gen > f.gen:
		// Renamed while WhatsApp was asked: the event's name is newer.
		f.name = e.name
	case err == nil:
		c.putLocked(jid, name, groupNameTTL)
	case ctx.Err() == nil:
		c.putLocked(jid, "", groupNameFailTTL)
	}
	close(f.done)
	return f.name
}

// set keeps name as jid's, from the group list or an event.
func (c *groupNameCache) set(jid, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putLocked(jid, name, groupNameTTL)
}

func (c *groupNameCache) putLocked(jid, name string, ttl time.Duration) {
	if c.names == nil {
		c.names = map[string]groupNameEntry{}
	}
	c.gen++
	c.names[jid] = groupNameEntry{name: name, until: time.Now().Add(ttl), gen: c.gen}
}
