package whatsapp

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/Icewater1337/fridolin/internal/chat"
)

// fakeGroups stands in for WhatsApp's answer to the joined-groups query:
// it counts the queries and answers each with what next held when it was
// asked; while held is set, only once the test releases it.
type fakeGroups struct {
	calls atomic.Int32
	next  atomic.Value // []GroupInfo
	fail  atomic.Bool
	held  atomic.Bool

	mu    sync.Mutex
	waits []chan struct{} // one per held query, in order
}

func newFakeGroups(names ...string) *fakeGroups {
	f := &fakeGroups{}
	f.set(names...)
	return f
}

func (f *fakeGroups) set(names ...string) {
	var groups []chat.Group
	for _, n := range names {
		groups = append(groups, chat.Group{JID: "120363000000000000@g.us", Name: n, Participants: 3})
	}
	f.next.Store(groups)
}

func (f *fakeGroups) fetch(ctx context.Context) ([]chat.Group, error) {
	f.calls.Add(1)
	groups := f.next.Load().([]chat.Group)
	if f.held.Load() {
		wait := make(chan struct{})
		f.mu.Lock()
		f.waits = append(f.waits, wait)
		f.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.fail.Load() {
		return nil, errors.New("timed out")
	}
	return groups, nil
}

// release lets the i-th held query (from 0) answer.
func (f *fakeGroups) release(i int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	close(f.waits[i])
}

func groupNames(groups []chat.Group) []string {
	var out []string
	for _, g := range groups {
		out = append(out, g.Name)
	}
	return out
}

// The dashboard asks for the groups every minute and after each change on
// the Chats page: those are answered from memory, and WhatsApp is asked
// again only once the list is old or an event dropped it.
func TestGroupCache(t *testing.T) {
	ask := func(t *testing.T, c *groupCache, f *fakeGroups, want ...string) {
		t.Helper()
		got, err := c.get(t.Context(), f.fetch)
		if err != nil || !slices.Equal(groupNames(got), want) {
			t.Fatalf("get = %v, %v; want %v", groupNames(got), err, want)
		}
	}
	queries := func(t *testing.T, f *fakeGroups, want int32) {
		t.Helper()
		if n := f.calls.Load(); n != want {
			t.Fatalf("%d queries to WhatsApp, want %d", n, want)
		}
	}

	t.Run("repeats come from memory until the list is old", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var c groupCache
			f := newFakeGroups("Family")
			ask(t, &c, f, "Family")
			f.set("Family", "Work")
			ask(t, &c, f, "Family")
			time.Sleep(groupsTTL - time.Second)
			ask(t, &c, f, "Family")
			queries(t, f, 1)
			time.Sleep(time.Second)
			ask(t, &c, f, "Family", "Work")
			queries(t, f, 2)
		})
	})

	t.Run("an invalidation asks again", func(t *testing.T) {
		var c groupCache
		f := newFakeGroups("Family")
		ask(t, &c, f, "Family")
		f.set("Family", "Work")
		c.invalidate()
		ask(t, &c, f, "Family", "Work")
		ask(t, &c, f, "Family", "Work")
		queries(t, f, 2)
	})

	t.Run("no groups is an answer too", func(t *testing.T) {
		var c groupCache
		f := newFakeGroups()
		ask(t, &c, f)
		ask(t, &c, f)
		queries(t, f, 1)
	})

	t.Run("a failure is not kept", func(t *testing.T) {
		var c groupCache
		f := newFakeGroups("Family")
		f.fail.Store(true)
		if _, err := c.get(t.Context(), f.fetch); err == nil {
			t.Fatal("the failure was not passed on")
		}
		f.fail.Store(false)
		ask(t, &c, f, "Family")
		queries(t, f, 2)
	})

	t.Run("requests during a query wait for it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var c groupCache
			f := newFakeGroups("Family")
			f.held.Store(true)
			got := make(chan []string, 3)
			for range 3 {
				go func() {
					groups, _ := c.get(context.Background(), f.fetch)
					got <- groupNames(groups)
				}()
			}
			synctest.Wait()
			queries(t, f, 1)
			f.release(0)
			for range 3 {
				if names := <-got; !slices.Equal(names, []string{"Family"}) {
					t.Errorf("a waiting request got %v", names)
				}
			}
		})
	})

	t.Run("a query from before an invalidation is not kept", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var c groupCache
			f := newFakeGroups("Family")
			f.held.Store(true)
			old := make(chan []string, 1)
			go func() {
				groups, _ := c.get(context.Background(), f.fetch)
				old <- groupNames(groups)
			}()
			synctest.Wait()

			// The account joins a group while WhatsApp is being asked.
			f.set("Family", "Work")
			c.invalidate()
			fresh := make(chan []string, 1)
			go func() {
				groups, _ := c.get(context.Background(), f.fetch)
				fresh <- groupNames(groups)
			}()
			synctest.Wait()
			queries(t, f, 2)
			// The older query answers last, and must not undo the newer.
			f.release(1)
			if names := <-fresh; !slices.Equal(names, []string{"Family", "Work"}) {
				t.Errorf("the request from after got %v", names)
			}
			f.release(0)
			if names := <-old; !slices.Equal(names, []string{"Family"}) {
				t.Errorf("the request from before got %v", names)
			}
			synctest.Wait()
			f.held.Store(false)
			ask(t, &c, f, "Family", "Work")
			queries(t, f, 2)
		})
	})

	t.Run("a request that gives up leaves the query to the others", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var c groupCache
			f := newFakeGroups("Family")
			f.held.Store(true)
			ctx, cancel := context.WithCancel(context.Background())
			gaveUp := make(chan error, 1)
			go func() {
				_, err := c.get(ctx, f.fetch)
				gaveUp <- err
			}()
			synctest.Wait() // its request started the query
			waited := make(chan []string, 1)
			go func() {
				groups, _ := c.get(context.Background(), f.fetch)
				waited <- groupNames(groups)
			}()
			synctest.Wait()
			cancel()
			if err := <-gaveUp; !errors.Is(err, context.Canceled) {
				t.Errorf("the request that gave up got %v", err)
			}
			f.release(0)
			if names := <-waited; !slices.Equal(names, []string{"Family"}) {
				t.Errorf("the other request got %v", names)
			}
			queries(t, f, 1)
		})
	})

	t.Run("a query that hangs ends", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var c groupCache
			f := newFakeGroups("Family")
			f.held.Store(true) // and never released
			if _, err := c.get(context.Background(), f.fetch); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("get = %v, want the query's deadline", err)
			}
		})
	})
}

// The events that can change the account's groups drop the list; the
// rest, messages above all, leave it.
func TestGroupEventsDropTheList(t *testing.T) {
	group := types.NewJID("120363000000000000", types.GroupServer)
	cases := []struct {
		name string
		evt  any
		drop bool
	}{
		{"joined a group", &events.JoinedGroup{}, true},
		{"renamed", &events.GroupInfo{JID: group, Name: &types.GroupName{Name: "Work"}}, true},
		{"someone left, or the account was removed", &events.GroupInfo{JID: group, Leave: []types.JID{types.NewJID("15551234567", types.DefaultUserServer)}}, true},
		{"connected again", &events.Connected{}, true},
		{"paired", &events.PairSuccess{}, true},
		{"logged out", &events.LoggedOut{}, true},
		{"a message", &events.Message{}, false},
		{"a receipt", &events.Receipt{}, false},
		{"disconnected", &events.Disconnected{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cache groupCache
			f := newFakeGroups("Family")
			if _, err := cache.get(t.Context(), f.fetch); err != nil {
				t.Fatal(err)
			}
			cache.observe(c.evt)
			if _, err := cache.get(t.Context(), f.fetch); err != nil {
				t.Fatal(err)
			}
			if dropped := f.calls.Load() == 2; dropped != c.drop {
				t.Errorf("dropped the list: %v, want %v", dropped, c.drop)
			}
		})
	}
}

// JoinedGroups, which the dashboard's /api/whatsapp/groups calls, answers
// from the client's cache, and the events whatsmeow hands the client's
// event handler are what drop it.
func TestJoinedGroupsAnswersFromTheCache(t *testing.T) {
	want := []chat.Group{{JID: "120363000000000000@g.us", Name: "Family", Participants: 3}}
	cases := []struct {
		name    string
		evt     any // handed to the event handler between the calls; nil for none
		queries int32
	}{
		{"two calls in a row", nil, 1},
		{"a message between them", &events.Message{}, 1},
		{"joined a group between them", &events.JoinedGroup{}, 2},
		{"a group changed between them", &events.GroupInfo{JID: types.NewJID("120363000000000000", types.GroupServer)}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tw := newTestWhatsApp(t)
			fl := &fakeLister{}
			tw.lister = fl
			for i := range 2 {
				if i == 1 && c.evt != nil {
					tw.HandleEvent(c.evt)
				}
				got, err := tw.JoinedGroups(t.Context())
				if err != nil || !slices.Equal(got, want) {
					t.Fatalf("call %d: JoinedGroups = %+v, %v; want %+v", i+1, got, err, want)
				}
			}
			if n := fl.calls.Load(); n != c.queries {
				t.Errorf("WhatsApp was asked %d times, want %d", n, c.queries)
			}
		})
	}
}

// fakeGroupName stands in for WhatsApp's answer to a group's info: it
// counts the lookups and answers each with name, or fails while fail is
// set; while held is set, it answers only once the test closes release.
type fakeGroupName struct {
	calls   atomic.Int32
	name    atomic.Value // string
	fail    atomic.Bool
	held    atomic.Bool
	release chan struct{}
}

func newFakeGroupName(name string) *fakeGroupName {
	f := &fakeGroupName{release: make(chan struct{})}
	f.name.Store(name)
	return f
}

func (f *fakeGroupName) fetch(ctx context.Context) (string, error) {
	f.calls.Add(1)
	name := f.name.Load().(string)
	if f.held.Load() {
		select {
		case <-f.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if f.fail.Load() {
		return "", errors.New("item-not-found")
	}
	return name, nil
}

// A group's name is asked of WhatsApp at most once per groupNameTTL, or
// per groupNameFailTTL after a lookup failed, however many messages ask
// for it; lookups of one group at once share one query, which has a
// timeout of its own.
func TestGroupNameCache(t *testing.T) {
	const jid = "120363000000000000@g.us"
	ask := func(t *testing.T, c *groupNameCache, f *fakeGroupName, want string) {
		t.Helper()
		if got := c.lookup(context.Background(), jid, f.fetch); got != want {
			t.Fatalf("lookup = %q, want %q", got, want)
		}
	}
	queries := func(t *testing.T, f *fakeGroupName, want int32) {
		t.Helper()
		if n := f.calls.Load(); n != want {
			t.Fatalf("%d lookups reached WhatsApp, want %d", n, want)
		}
	}

	t.Run("once per TTL", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var c groupNameCache
			f := newFakeGroupName("Family")
			for range 10 {
				ask(t, &c, f, "Family")
			}
			f.name.Store("Family 2026")
			time.Sleep(groupNameTTL - time.Second)
			ask(t, &c, f, "Family")
			queries(t, f, 1)
			time.Sleep(time.Second)
			ask(t, &c, f, "Family 2026")
			queries(t, f, 2)
		})
	})

	t.Run("a failure is kept for a few minutes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var c groupNameCache
			f := newFakeGroupName("Family")
			f.fail.Store(true)
			for range 10 {
				ask(t, &c, f, "")
			}
			queries(t, f, 1)
			f.fail.Store(false)
			time.Sleep(groupNameFailTTL)
			ask(t, &c, f, "Family")
			queries(t, f, 2)
		})
	})

	t.Run("lookups at once share one query", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var c groupNameCache
			f := newFakeGroupName("Family")
			f.held.Store(true)
			got := make(chan string, 3)
			for range 3 {
				go func() { got <- c.lookup(context.Background(), jid, f.fetch) }()
			}
			synctest.Wait()
			queries(t, f, 1)
			close(f.release)
			for range 3 {
				if name := <-got; name != "Family" {
					t.Errorf("a lookup got %q", name)
				}
			}
		})
	})

	t.Run("a lookup that hangs ends", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var c groupNameCache
			f := newFakeGroupName("Family")
			f.held.Store(true) // and never released
			start := time.Now()
			ask(t, &c, f, "")
			if waited := time.Since(start); waited != groupNameTimeout {
				t.Errorf("the lookup took %v, want %v", waited, groupNameTimeout)
			}
			ask(t, &c, f, "") // kept as a failure
			queries(t, f, 1)
		})
	})

	t.Run("a lookup cut off by shutting down is not kept", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var c groupNameCache
			f := newFakeGroupName("Family")
			f.held.Store(true)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan string)
			go func() { done <- c.lookup(ctx, jid, f.fetch) }()
			synctest.Wait()
			cancel()
			if name := <-done; name != "" {
				t.Errorf("lookup = %q, want none", name)
			}
			f.held.Store(false)
			ask(t, &c, f, "Family")
			queries(t, f, 2)
		})
	})

	t.Run("a rename during a lookup wins", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var c groupNameCache
			f := newFakeGroupName("Family")
			f.held.Store(true)
			done := make(chan string)
			go func() { done <- c.lookup(context.Background(), jid, f.fetch) }()
			synctest.Wait()
			c.set(jid, "Family 2026")
			close(f.release)
			if name := <-done; name != "Family 2026" {
				t.Errorf("lookup = %q, want the new name", name)
			}
			ask(t, &c, f, "Family 2026")
			queries(t, f, 1)
		})
	})

	t.Run("cached never asks", func(t *testing.T) {
		var c groupNameCache
		if name := c.cached(jid); name != "" {
			t.Errorf("cached = %q, want none", name)
		}
		c.set(jid, "Family")
		if name := c.cached(jid); name != "Family" {
			t.Errorf("cached = %q, want Family", name)
		}
	})
}

// The names come from the group list whenever it is loaded, and from the
// events that name a group: those are not asked of WhatsApp again.
func TestGroupNamesFromTheListAndEvents(t *testing.T) {
	group := types.NewJID("120363000000000000", types.GroupServer)
	jid := group.String()
	t.Run("the list", func(t *testing.T) {
		var c groupCache
		if _, err := c.get(t.Context(), newFakeGroups("Family").fetch); err != nil {
			t.Fatal(err)
		}
		f := newFakeGroupName("not asked")
		if name := c.names.lookup(t.Context(), jid, f.fetch); name != "Family" || f.calls.Load() != 0 {
			t.Errorf("lookup = %q after %d queries, want Family from the list", name, f.calls.Load())
		}
	})
	cases := []struct {
		name string
		evt  any
		want string
	}{
		{"renamed", &events.GroupInfo{JID: group, Name: &types.GroupName{Name: "Work"}}, "Work"},
		{"joined", &events.JoinedGroup{GroupInfo: types.GroupInfo{JID: group, GroupName: types.GroupName{Name: "Work"}}}, "Work"},
		{"another change keeps the name", &events.GroupInfo{JID: group, Leave: []types.JID{types.NewJID("15551234567", types.DefaultUserServer)}}, "Family"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cache groupCache
			cache.names.set(jid, "Family")
			cache.observe(c.evt)
			if name := cache.names.cached(jid); name != c.want {
				t.Errorf("name = %q, want %q", name, c.want)
			}
		})
	}
}
