package config

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestFieldsCoverEveryJSONLeafAndBootValue(t *testing.T) {
	t.Parallel()
	leaves := map[string]bool{}
	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, prefix string) {
		for _, f := range reflect.VisibleFields(typ) {
			key := strings.Split(f.Tag.Get("json"), ",")[0]
			if key == "" || key == "-" {
				continue
			}
			name := prefix + key
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct && !reflect.PointerTo(ft).Implements(reflect.TypeFor[json.Unmarshaler]()) {
				walk(ft, name+".")
			} else {
				leaves[name] = true
			}
		}
	}
	walk(reflect.TypeFor[File](), "")
	seen := map[string]bool{}
	boot := map[string]bool{}
	for _, f := range Fields() {
		if seen[f.Name] {
			t.Errorf("duplicate field row %q", f.Name)
		}
		seen[f.Name] = true
		if !leaves[f.Name] {
			t.Errorf("field row %q has no JSON leaf", f.Name)
		}
		delete(leaves, f.Name)
		if f.Boot {
			boot[f.Name] = true
		}
	}
	for name := range leaves {
		t.Errorf("JSON leaf %q has no field row", name)
	}
	for _, f := range reflect.VisibleFields(reflect.TypeFor[Boot]()) {
		key := f.Tag.Get("config")
		if key == "-" {
			continue
		}
		if !boot[key] {
			t.Errorf("Boot.%s maps to non-Boot field %q", f.Name, key)
		}
		delete(boot, key)
	}
	for name := range boot {
		t.Errorf("Boot field row %q has no Boot value", name)
	}
	// Callers cannot change the inventory or the restart reporting order.
	owned := Fields()
	owned[0].Name = "changed"
	want := []string{"http_listen", "data_dir", "db_path", "media_dir", "log_level", "dashboard.disabled", "dashboard.require_token"}
	if !slices.Equal(RestartFieldNames(), want) {
		t.Fatalf("restart field order = %v, want %v", RestartFieldNames(), want)
	}
}

func TestRestartRequiredOrderAndTokenTransitions(t *testing.T) {
	t.Parallel()
	old := &File{HTTPListen: "old", DataDir: "old", DBPath: "old", MediaDir: "old", LogLevel: "info"}
	next := &File{HTTPListen: "new", DataDir: "new", DBPath: "new", MediaDir: "new", LogLevel: "debug", Dashboard: DashboardConfig{Disabled: true, RequireToken: new(false)}}
	if got := restartRequired(old, next); !slices.Equal(got, RestartFieldNames()) {
		t.Fatalf("restart = %v", got)
	}
	for _, start := range []*bool{nil, new(false), new(true)} {
		for _, now := range []*bool{nil, new(false), new(true)} {
			a := &File{Dashboard: DashboardConfig{RequireToken: start}}
			b := &File{Dashboard: DashboardConfig{RequireToken: now}}
			got := restartRequired(a, b)
			want := a.DashboardRequiresToken() && !b.DashboardRequiresToken()
			if (len(got) != 0) != want || len(got) > 0 && got[0] != "dashboard.require_token" {
				t.Fatalf("token transition %v -> %v: %v", a.DashboardRequiresToken(), b.DashboardRequiresToken(), got)
			}
		}
	}
}
