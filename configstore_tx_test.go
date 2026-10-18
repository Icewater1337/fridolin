package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// openTxStore opens a config store on a config.json with body, in a temp
// HOME of its own.
func openTxStore(t *testing.T, body string) *ConfigStore {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cs, err := OpenConfigStore(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

// A panic in an update is an error, and leaves the store as it was and
// free for the next update: net/http would recover the handler's panic,
// and a lock left held would block every later save.
func TestConfigUpdatePanicIsAnError(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config) error
	}{
		{"in mutate", func(c *Config) error {
			c.LogLevel = "debug"
			panic("boom")
		}},
		{"out of range", func(c *Config) error {
			c.LogLevel = c.AllowedNumbers[len(c.AllowedNumbers)]
			return nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := openTxStore(t, `{"allowed_numbers":["15551234567"]}`)
			before, err := os.ReadFile(cs.Path())
			if err != nil {
				t.Fatal(err)
			}
			etag := cs.ETag()
			done := make(chan error, 1)
			go func() {
				_, err := cs.Update("", tc.mutate)
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "the config update failed") {
					t.Errorf("Update with a panic: err = %v, want the panic as an error", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Update with a panic did not return")
			}
			if cs.ETag() != etag || cs.Get().LogLevel != "info" {
				t.Errorf("after the panic: etag %q (was %q), log_level %q: the update was applied", cs.ETag(), etag, cs.Get().LogLevel)
			}
			if after, _ := os.ReadFile(cs.Path()); string(after) != string(before) {
				t.Errorf("config.json changed:\n%s", after)
			}

			next := make(chan error, 1)
			go func() {
				_, err := cs.Update("", func(c *Config) error { c.LogLevel = "warn"; return nil })
				next <- err
			}()
			select {
			case err := <-next:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the update after the panic blocks: the lock was left held")
			}
			if got := cs.Get().LogLevel; got != "warn" {
				t.Errorf("log_level = %q after the next update, want warn", got)
			}
			_ = cs.FileCopy() // and so does reading the file layer
		})
	}
}

// GET /api/config sends the etag of the config it sends, also while other
// updates commit: with an etag of a later write, a save of the page's
// stale config would pass the conflict check and undo that write.
func TestGetConfigSendsTheETagOfItsConfig(t *testing.T) {
	tb := newTestBridge(t, "")
	// The etag is the hash of config.json, which is the file layer GET
	// sends, indented; without brains, brain_dirs is null in both.
	check := func() error {
		w := tb.do("GET", "/api/config", nil)
		var v struct {
			ETag   string          `json:"etag"`
			Config json.RawMessage `json:"config"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			return fmt.Errorf("GET /api/config: %d %s", w.Code, w.Body.String())
		}
		var c Config
		if err := json.Unmarshal(v.Config, &c); err != nil {
			return err
		}
		data, err := json.MarshalIndent(&c, "", "  ")
		if err != nil {
			return err
		}
		if got := etagOf(append(data, '\n')); got != v.ETag {
			return fmt.Errorf("etag %s with a config whose hash is %s (_comment %q)", v.ETag, got, c.Comment)
		}
		return nil
	}
	if _, err := tb.cfgs.Update("", func(c *Config) error { c.Comment = "start"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := check(); err != nil {
		t.Fatalf("without other updates: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := tb.cfgs.Update("", func(c *Config) error { c.Comment = fmt.Sprintf("w%d-%d", w, i); return nil }); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	deadline := time.Now().Add(time.Second)
	for n := 0; n < 2000 && time.Now().Before(deadline); n++ {
		if err := check(); err != nil {
			t.Error(err)
			break
		}
	}
	close(stop)
	wg.Wait()
}

// The subscribers hear of the updates in the order they were made, one at a
// time: one that applies what it hears, as the brains do, must not end on
// a configuration a later update replaced because the later notification
// overtook the earlier one.
func TestConfigSubscribersHearUpdatesInOrder(t *testing.T) {
	cs := openTxStore(t, `{}`)
	var mu sync.Mutex
	var heard []string
	inA := make(chan struct{})
	heardB := make(chan struct{})
	cs.Subscribe(func(_, next *Config) {
		if next.Comment == "a" {
			close(inA)
			// B commits meanwhile; its notification must wait for this one.
			waitFor(t, 5*time.Second, "update b to commit", func() bool { return cs.Get().Comment == "b" })
			select {
			case <-heardB:
			case <-time.After(200 * time.Millisecond):
			}
		}
		mu.Lock()
		heard = append(heard, next.Comment)
		mu.Unlock()
		if next.Comment == "b" {
			close(heardB)
		}
	})
	done := make(chan error, 2)
	go func() {
		_, err := cs.Update("", func(c *Config) error { c.Comment = "a"; return nil })
		done <- err
	}()
	<-inA
	go func() {
		_, err := cs.Update("", func(c *Config) error { c.Comment = "b"; return nil })
		done <- err
	}()
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(heard, ",") != "a,b" {
		t.Errorf("the subscriber heard of the updates as %v, want [a b]", heard)
	}
}

// A subscriber that panics is logged; the update stays saved, the other
// subscribers hear of it, and the next update is not blocked.
func TestConfigSubscriberPanicIsLogged(t *testing.T) {
	cs := openTxStore(t, `{}`)
	logs := captureLog(t)
	cs.Subscribe(func(_, next *Config) {
		if next.Comment == "boom" {
			panic("subscriber")
		}
	})
	var heard []string
	cs.Subscribe(func(_, next *Config) { heard = append(heard, next.Comment) })
	if _, err := cs.Update("", func(c *Config) error { c.Comment = "boom"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Update("", func(c *Config) error { c.Comment = "next"; return nil }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(heard, ",") != "boom,next" || cs.Get().Comment != "next" {
		t.Errorf("heard %v, config %q", heard, cs.Get().Comment)
	}
	if !logContains(logs, "a subscriber panicked on the saved update: subscriber") {
		t.Errorf("the panic was not logged: %q", logs())
	}
}

// After updates that run at once, the brains run on the configuration the
// store has, not on one an update replaced.
func TestBrainsRunOnTheLatestConfig(t *testing.T) {
	h := newHarness(t, "")
	h.AddBrain(BrainManifest{ID: "main", Name: "Main", Routing: BrainRouting{HandlesDMs: true}})
	for round := 0; round < 5; round++ {
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				m := BrainManifest{ID: "main", Name: fmt.Sprintf("Main %d-%d", round, i), Routing: BrainRouting{HandlesDMs: true}}
				if w := h.Do("PUT", "/api/brains/main", map[string]any{"manifest": m}); w.Code != 200 {
					t.Errorf("PUT /api/brains/main: %d %s", w.Code, w.Body.String())
				}
			}()
		}
		wg.Wait()
		var views []brainView
		if w := h.Do("GET", "/api/brains", nil); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &views) != nil {
			t.Fatalf("GET /api/brains: %d %s", w.Code, w.Body.String())
		}
		if len(views) != 1 || views[0].Runtime == nil {
			t.Fatalf("GET /api/brains = %+v", views)
		}
		approved := h.Approved()["main"].Name
		if views[0].Name != approved || views[0].Runtime.Name != approved {
			t.Fatalf("round %d: config.json approves %q, the config %q, but the brain runs as %q", round, approved, views[0].Name, views[0].Runtime.Name)
		}
	}
}

// The bridge applies the configuration the store has when it hears of an
// update, not the one the notification carries: a notification that comes
// after a later update's leaves the brains on the later one.
func TestBridgeAppliesTheLatestConfigOnALateNotification(t *testing.T) {
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, BrainManifest{ID: "main", Name: "Main", Routing: BrainRouting{HandlesDMs: true}})
	rename := func(name string) ConfigSnapshot {
		t.Helper()
		if err := tb.cfgs.UpdateManifest("main", func(m *BrainManifest) error { m.Name = name; return nil }); err != nil {
			t.Fatal(err)
		}
		return tb.cfgs.Snapshot()
	}
	before := tb.cfgs.Snapshot()
	first := rename("First")
	rename("Second")
	tb.cfgs.subsMu.Lock()
	subs := slices.Clone(tb.cfgs.subs)
	tb.cfgs.subsMu.Unlock()
	for _, fn := range subs {
		fn(before.Effective, first.Effective) // the first update's, late
	}
	if got := tb.brainMgr.Get("main").Snapshot().Name; got != "Second" {
		t.Errorf("after a late notification the brain runs as %q, want Second", got)
	}
}

// PUT /api/config answers with the etag of the config.json it wrote, also
// when another update has written one since: with that one's etag, the
// page would take the other write for its own and save over it.
func TestPutConfigSendsItsOwnETag(t *testing.T) {
	tb := newTestBridge(t, "")
	var once sync.Once
	var putETag string
	later := make(chan error, 1)
	tb.cfgs.Subscribe(func(_, next *Config) {
		if next.LogLevel != "debug" {
			return
		}
		// Heard of the PUT's update: another one commits before the PUT
		// answers. It waits to tell its subscribers until this returns.
		once.Do(func() {
			putETag = tb.cfgs.ETag()
			go func() {
				_, err := tb.cfgs.Update("", func(c *Config) error { c.Comment = "later"; return nil })
				later <- err
			}()
			waitFor(t, 5*time.Second, "the later update", func() bool { return tb.cfgs.Get().Comment == "later" })
		})
	})
	w := tb.editConfig(t, func(c map[string]any) { c["log_level"] = "debug" })
	if err := <-later; err != nil {
		t.Fatal(err)
	}
	var resp struct {
		ETag string `json:"etag"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &resp) != nil {
		t.Fatalf("PUT /api/config: %d %s", w.Code, w.Body.String())
	}
	if resp.ETag != putETag || resp.ETag == tb.cfgs.ETag() {
		t.Errorf("PUT answered etag %s; it wrote %s, the later update %s", resp.ETag, putETag, tb.cfgs.ETag())
	}
	// Saving again with it is a conflict: the page has not seen the later
	// write.
	if w := tb.do("PUT", "/api/config", map[string]any{"etag": resp.ETag, "config": tb.cfgs.FileCopy()}); w.Code != 409 {
		t.Errorf("save with the PUT's etag after a later write: %d %s, want 409", w.Code, w.Body.String())
	}
}
