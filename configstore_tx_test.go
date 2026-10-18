package main

import (
	"encoding/json"
	"fmt"
	"os"
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
