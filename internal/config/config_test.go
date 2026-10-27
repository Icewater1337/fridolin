package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// config.json carries the version it was written in. A file without one is
// version 1, which is migrated to the current version as it is read; a
// newer version, or a version that is no number, is refused.
func TestMigrateConfig(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, config string
		from         int
		want         map[string]string // keys of the migrated file, as JSON; "" for a key it lacks
		err          string            // part of the error, "" when it migrates
	}{
		{"no version", `{"log_level": "debug"}`, 1, map[string]string{"version": "3", "log_level": `"debug"`}, ""},
		{"null", `{"version": null}`, 1, map[string]string{"version": "3"}, ""},
		{"version 1 with retired keys", `{"version": 1, "model": "sonnet", "work_dir": "~/a", "brains": []}`, 1,
			map[string]string{"version": "3", "brain_model": `"sonnet"`, "model": "", "work_dir": "", "brains": ""}, ""},
		{"version 1 keeps its brain_model", `{"model": "sonnet", "brain_model": "opus"}`, 1, map[string]string{"brain_model": `"opus"`, "model": ""}, ""},
		// The retired keys are version 1's: in a version 2 file they are
		// unknown keys, which no migration touches.
		{"version 2", `{"version": 2, "model": "sonnet"}`, 2, map[string]string{"version": "3", "model": `"sonnet"`, "brain_model": ""}, ""},
		// Docker Sandboxes for brains were removed: version 3 drops their
		// keys and keeps the rest of sandbox.
		{"version 2 with the brain runtime", `{"version": 2, "sandbox": {"enabled": true, "runtime": "docker-sandbox", "node": "mini"}}`, 2,
			map[string]string{"version": "3", "sandbox": `{"enabled":true}`}, ""},
		{"version 1 with the brain runtime", `{"sandbox": {"runtime": "bubblewrap"}}`, 1, map[string]string{"sandbox": `{}`}, ""},
		{"version 2 with a sandbox of another type", `{"version": 2, "sandbox": true}`, 2, map[string]string{"version": "3", "sandbox": "true"}, ""},
		{"version 3 keeps unknown sandbox keys", `{"version": 3, "sandbox": {"runtime": "docker-sandbox"}}`, 3,
			map[string]string{"sandbox": `{"runtime": "docker-sandbox"}`}, ""},
		{"version 2 with an approved brain sandbox", `{"version": 2, "brain_dirs": [{"path": "/x", "approved": {"id": "a", "sandbox": {"runtime": "docker-sandbox"}}}, {"path": "/y"}]}`, 2,
			map[string]string{"brain_dirs": `[{"approved":{"id":"a"},"path":"/x"},{"path":"/y"}]`}, ""},
		{"version 1 with inline brains", `{"brains": [{"name": "a"}]}`, 1, nil, `lists brains inline under "brains"`},
		{"newer", `{"version": 4}`, 4, nil, "version 4 is newer than the version 3 this build of Fridolin reads"},
		{"zero", `{"version": 0}`, 0, nil, "version 0: must be a whole number from 1 to 3"},
		{"negative", `{"version": -1}`, 0, nil, "version -1: must be a whole number from 1 to 3"},
		{"a string", `{"version": "2"}`, 0, nil, `version "2": must be a whole number`},
		{"a fraction", `{"version": 1.5}`, 0, nil, "version 1.5: must be a whole number"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal([]byte(c.config), &obj); err != nil {
				t.Fatal(err)
			}
			from, _, err := migrateConfig(obj)
			switch {
			case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
				t.Fatalf("err = %v, want one containing %q", err, c.err)
			case c.err == "" && err != nil:
				t.Fatalf("err = %v", err)
			}
			if from != c.from {
				t.Errorf("from = %d, want %d", from, c.from)
			}
			for key, want := range c.want {
				if got := string(obj[key]); got != want {
					t.Errorf("%s = %s, want %s", key, got, want)
				}
			}
		})
	}
	if len(configMigrations) != Version-1 {
		t.Errorf("%d migrations for version %d: each version after 1 needs one", len(configMigrations), Version)
	}
}

func TestBackupConfigValidate(t *testing.T) {
	t.Parallel()
	for _, bad := range []BackupConfig{{DailyAt: "25:00"}, {Remote: "my remote"}, {Root: "../x"}, {Root: "/abs"}, {Root: "a:b"}} {
		if bad.Validate() == nil {
			t.Errorf("%+v should be rejected", bad)
		}
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	t.Parallel()
	loopback := []string{"127.0.0.1:8765", "localhost:8765", "[::1]:8765"}
	for _, a := range loopback {
		if !IsLoopbackAddr(a) {
			t.Errorf("IsLoopbackAddr(%q) = false, want true", a)
		}
	}
	exposed := []string{"0.0.0.0:8765", ":8765", "192.168.1.5:8765", "example.com:80", "8765"}
	for _, a := range exposed {
		if IsLoopbackAddr(a) {
			t.Errorf("IsLoopbackAddr(%q) = true, want false", a)
		}
	}
}
