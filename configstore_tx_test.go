package main

import (
	"os"
	"strings"
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
