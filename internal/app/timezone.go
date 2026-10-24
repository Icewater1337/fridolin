package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
)

// zone is the owner's time zone (timezone), else the process's: the zone
// of every time of day the bridge keeps. Read it rather than time.Local,
// which in the core container is whatever TZ the container got.
func (b *App) zone() *time.Location {
	return b.cfgs.Get().Live.Location()
}

// The sources of the zone the bridge reads times of day in.
const (
	zoneFromConfig  = "config"
	zoneFromProcess = "process"
)

// timezoneView is the zone the bridge reads times of day in, for the
// dashboard: its IANA name, and whether config's timezone set it or the
// process's own zone (TZ, else the system's) stands in.
type timezoneView struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

func zoneView(cfg *config.Settings) timezoneView {
	if cfg.Timezone != "" {
		return timezoneView{Name: cfg.Location().String(), Source: zoneFromConfig}
	}
	return timezoneView{Name: processZoneName(), Source: zoneFromProcess}
}

// processZoneName names the process's zone: TZ when it names one, else
// time.Local's, which Go calls "Local".
func processZoneName() string {
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" {
		return tz
	}
	return time.Local.String()
}

// ownerZone is config's timezone, nil when it is unset (brain.Settings
// then leaves runs in the process's zone).
func ownerZone(cfg *config.Settings) *time.Location {
	if cfg.Timezone == "" {
		return nil
	}
	return cfg.Location()
}

// zoneEnv is the TZ a brain's run gets: the owner's zone, so that its
// clock, its calendar tool and the scripts it runs agree with the
// bridge's schedules. None when timezone is unset: the run inherits the
// bridge's own TZ, if it has one.
func zoneEnv(name string) []string {
	if name == "" {
		return nil
	}
	return []string{"TZ=" + name}
}

// utcZone reports whether a zone name means UTC.
func utcZone(name string) bool {
	switch strings.TrimPrefix(name, ":") {
	case "UTC", "Etc/UTC", "UTC0", "Etc/UCT", "UCT", "GMT", "Etc/GMT", "Zulu", "Etc/Zulu":
		return true
	}
	return false
}

// checkTimezone is doctor's "timezone" check: the zone schedules are read
// in, and a warning when it is likely not the owner's: unset with the
// process in UTC (the core container's default), or set but the
// container's TZ another zone.
func checkTimezone(_ context.Context, env *DoctorEnv) DoctorResult {
	tz := strings.TrimPrefix(strings.TrimSpace(env.Getenv("TZ")), ":")
	const fixUnset = `set "timezone" in config.json to your IANA zone (such as Europe/Zurich), on the dashboard under Settings; for the core, fridolin core start also passes the host's zone into the container as TZ`
	if env.Config == nil {
		return DoctorResult{Status: DoctorMissing, Detail: "config did not load"}
	}
	if name := env.Config.Timezone; name != "" {
		// In the core's container the entrypoint gives the bridge this zone
		// as TZ (#250); doctor, run through docker exec, has the
		// container's own TZ instead.
		if tz != "" && tz != name && env.ConfigGOOS != config.ContainerGOOS {
			return DoctorResult{Status: DoctorWarn,
				Detail: fmt.Sprintf("schedules run in %s (timezone), but the process's TZ is %s: programs that read the clock themselves see another zone", name, tz),
				Fix:    "start the core with fridolin core start, which passes the host's zone as TZ (FRIDOLIN_TZ sets another), or set timezone to the zone you mean"}
		}
		return DoctorResult{Status: DoctorOK, Detail: name + " (timezone)"}
	}
	switch {
	case utcZone(tz), tz == "" && env.ConfigGOOS == config.ContainerGOOS:
		return DoctorResult{Status: DoctorWarn,
			Detail: "timezone is unset and the bridge runs in UTC: a \"daily 21:00\" agent runs at 21:00 UTC",
			Fix:    fixUnset}
	case tz != "":
		return DoctorResult{Status: DoctorOK, Detail: tz + " (TZ; timezone is unset)"}
	}
	return DoctorResult{Status: DoctorOK, Detail: "the system's zone (timezone is unset)"}
}
