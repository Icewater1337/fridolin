package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/doctor"
)

func TestCheckTimezone(t *testing.T) {
	t.Parallel()
	withZone := func(zone string) *config.Settings {
		c := &config.Settings{}
		c.Timezone = zone
		return c
	}
	cases := []struct {
		name, tz, goos string
		cfg            *config.Settings
		status, detail string
	}{
		{"set and TZ agrees", "Europe/Zurich", "darwin", withZone("Europe/Zurich"), doctor.OK, "Europe/Zurich (timezone)"},
		{"set, TZ unset", "", config.ContainerGOOS, withZone("Europe/Zurich"), doctor.OK, "Europe/Zurich (timezone)"},
		{"set, TZ another zone", "UTC", "linux", withZone("Europe/Zurich"), doctor.Warn, "but the process's TZ is UTC"},
		// The container's entrypoint gives the bridge timezone as TZ (#250);
		// doctor through docker exec has the container's own.
		{"set, the container's TZ another zone", "Etc/UTC", config.ContainerGOOS, withZone("Europe/Zurich"), doctor.OK, "Europe/Zurich (timezone)"},
		{"unset in UTC", "UTC", config.ContainerGOOS, withZone(""), doctor.Warn, "runs in UTC"},
		{"unset, no TZ in the container", "", config.ContainerGOOS, withZone(""), doctor.Warn, "runs in UTC"},
		{"unset, TZ the owner's", "Europe/Zurich", config.ContainerGOOS, withZone(""), doctor.OK, "Europe/Zurich (TZ; timezone is unset)"},
		{"unset, native", "", "darwin", withZone(""), doctor.OK, "the system's zone"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := &doctor.Env{ConfigGOOS: c.goos, Config: c.cfg, Getenv: func(k string) string {
				if k == "TZ" {
					return c.tz
				}
				return ""
			}}
			r := checkTimezone(context.Background(), env)
			if r.Status != c.status || !strings.Contains(r.Detail, c.detail) {
				t.Errorf("got %s %q, want %s with %q", r.Status, r.Detail, c.status, c.detail)
			}
			if r.Status != doctor.OK && r.Fix == "" {
				t.Error("a warning without its fix")
			}
		})
	}
}

// The owner's zone reaches brain runs as TZ and as the "now" of their
// prompt; unset, runs keep the process's.
func TestOwnerZone(t *testing.T) {
	t.Parallel()
	c := &config.Settings{}
	if ownerZone(c) != nil || zoneEnv("") != nil {
		t.Error("an unset timezone sets a zone")
	}
	if v := zoneView(c); v.Source != zoneFromProcess {
		t.Errorf("unset: %+v", v)
	}
	c.Timezone = "Europe/Zurich"
	if z := ownerZone(c); z == nil || z.String() != "Europe/Zurich" {
		t.Errorf("ownerZone = %v", z)
	}
	if v := zoneView(c); v != (timezoneView{Name: "Europe/Zurich", Source: zoneFromConfig}) {
		t.Errorf("zoneView = %+v", v)
	}
	if e := zoneEnv("Europe/Zurich"); len(e) != 1 || e[0] != "TZ=Europe/Zurich" {
		t.Errorf("zoneEnv = %v", e)
	}
	if c.Location().String() != "Europe/Zurich" || (&config.Settings{}).Location() != time.Local {
		t.Error("Location")
	}
}
