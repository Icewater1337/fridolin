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
	groups []GroupInfo // nil when not known
	at     time.Time   // when groups came
	gen    uint64      // counts invalidations
	flight *groupFetch // the query running, if any
}

// groupFetch is one query for the list; done is closed once groups and
// err hold its answer.
type groupFetch struct {
	gen    uint64 // the generation it was started in
	done   chan struct{}
	groups []GroupInfo
	err    error
}

// get returns the list, from fetch when it is not known or older than
// groupsTTL. A failed fetch is not kept, so the next call asks again.
func (c *groupCache) get(ctx context.Context, fetch func(context.Context) ([]GroupInfo, error)) ([]GroupInfo, error) {
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

func (c *groupCache) run(ctx context.Context, f *groupFetch, fetch func(context.Context) ([]GroupInfo, error)) {
	ctx, cancel := context.WithTimeout(ctx, groupsFetchTimeout)
	defer cancel()
	groups, err := fetch(ctx)
	if err == nil && groups == nil {
		groups = []GroupInfo{} // known to be none
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
// and a pairing or a logout, after which it may be another account.
func (c *groupCache) observe(evt any) {
	switch evt.(type) {
	case *events.JoinedGroup, *events.GroupInfo, *events.Connected, *events.PairSuccess, *events.LoggedOut:
		c.invalidate()
	}
}

// ForgetGroups drops the list the client keeps, so the next JoinedGroups
// asks WhatsApp: for the dashboard's Refresh button, which should also
// show a change that came without an event.
func (wa *WhatsAppClient) ForgetGroups() { wa.groups.invalidate() }

// groupLister is what JoinedGroups needs from whatsmeow.Client; tests swap
// in a fake to count what reaches WhatsApp.
type groupLister interface {
	IsLoggedIn() bool
	GetJoinedGroups(ctx context.Context) ([]*types.GroupInfo, error)
}

// groupsFrom is what JoinedGroups asks: the client, read anew each time
// since a logout replaces it, unless a test set lister.
func (wa *WhatsAppClient) groupsFrom() groupLister {
	if wa.lister != nil {
		return wa.lister
	}
	return wa.cl()
}
