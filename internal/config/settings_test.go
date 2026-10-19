package config

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestSettingsPreserveEffectiveWire(t *testing.T) {
	data, err := os.ReadFile("../../cmd/fridolin/testdata/config-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture File
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for name, file := range map[string]*File{"empty": {}, "fixture": &fixture, "explicit zero": {AllowAnyDM: new(true), RunTimeoutMinutes: new(0), MaxConcurrentRuns: new(0), Queue: QueueConfig{Notify: new(false)}, Dashboard: DashboardConfig{RequireToken: new(false)}, ScriptAPI: ScriptAPIConfig{TokenlessSend: new(false)}, Backup: BackupConfig{KeepVersionsDays: new(0), IncludeBridgeConfig: new(false)}, Debounce: DebounceConfig{QuietMS: new(0), MaxWaitMS: new(0)}}} {
		t.Run(name, func(t *testing.T) {
			paths := Paths{Home: t.TempDir()}
			old, err := resolveFile(file, paths, "darwin")
			if err != nil {
				t.Fatal(err)
			}
			settings, err := Resolve(file, paths, "darwin")
			if err != nil {
				t.Fatal(err)
			}
			got := EffectiveFile(settings)
			wantJSON, _ := json.Marshal(old)
			gotJSON, _ := json.Marshal(got)
			if string(wantJSON) != string(gotJSON) {
				t.Fatalf("effective wire changed:\nwant %s\ngot %s", wantJSON, gotJSON)
			}
			if settings.Live.AllowAnyDM != *old.AllowAnyDM || settings.Live.Dashboard.RequireToken != *old.Dashboard.RequireToken || settings.Live.Sandbox.Enabled != old.Sandbox.Enabled || settings.Live.DangerouslySkipPermissions != old.DangerouslySkipPermissions {
				t.Fatal("security policy changed")
			}
			if settings.Live.Plaud.Interval <= 0 {
				t.Fatal("unresolved poll interval")
			}
			if settings.Live.RunTimeout != time.Duration(*old.RunTimeoutMinutes)*time.Minute {
				t.Fatal("timeout units changed")
			}
			if settings.Live.Debounce.Quiet != time.Duration(*old.Debounce.QuietMS)*time.Millisecond {
				t.Fatal("debounce units changed")
			}
		})
	}
}

func TestSettingsPolicyHasNoOptionalPointers(t *testing.T) {
	var check func(reflect.Type)
	check = func(typ reflect.Type) {
		if typ.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			// BrainDirs retain approval documents, whose optional fields describe
			// inheritance in brain.json; they are not global runtime policy.
			if f.Name == "BrainDirs" {
				continue
			}
			if f.Type.Kind() == reflect.Pointer {
				t.Errorf("%s.%s remains optional", typ.Name(), f.Name)
			}
			check(f.Type)
		}
	}
	check(reflect.TypeFor[Settings]())
}

func TestRuntimeSettingsPublishAtomically(t *testing.T) {
	home := t.TempDir()
	cs, err := Open(home+"/config.json", Options{Paths: Paths{Home: home}, GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	first := cs.Get()
	calls := 0
	cs.Subscribe(func(old, next *Settings) {
		calls++
		if old.Live.AllowAnyDM || !next.Live.AllowAnyDM || next.Live.Queue.Notify {
			t.Fatal("subscriber policy does not match saved values")
		}
		snap := cs.Snapshot()
		if snap.Settings != cs.Get() || snap.Settings.Live.AllowAnyDM != *snap.Effective.AllowAnyDM {
			t.Fatal("runtime and effective snapshots diverged")
		}
	})
	_, err = cs.Update("", func(f *File) error { f.AllowAnyDM = new(true); f.Queue.Notify = new(false); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || first.Live.AllowAnyDM || !first.Live.Queue.Notify {
		t.Fatal("publication changed previous snapshot")
	}
	_, err = cs.Update("", func(f *File) error { f.HTTPListen = "not an address"; return nil })
	if err == nil || calls != 1 {
		t.Fatal("invalid update was published")
	}
}

func TestSettingsRejectUnrepresentableDurations(t *testing.T) {
	for name, file := range map[string]*File{
		"negative debounce": {Debounce: DebounceConfig{QuietMS: new(-1)}},
		"overflow debounce": {Debounce: DebounceConfig{MaxWaitMS: new(int(^uint(0) >> 1))}},
		"overflow poll":     {Plaud: PlaudConfig{IntervalSeconds: int(^uint(0) >> 1)}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Resolve(file, Paths{Home: t.TempDir()}, "linux"); err == nil {
				t.Fatal("invalid duration accepted")
			}
		})
	}
}
