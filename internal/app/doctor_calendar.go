package app

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/google"
)

// The calendar grants' doctor check: a brain whose Google sign-in Google
// refused, or that needs a new consent, has no calendar until the owner
// connects it again.
var calendarDoctorChecks = []DoctorCheck{
	{ID: "calendar-grants", Title: "brains' Google Calendar sign-ins work", Run: checkCalendarGrants},
}

func init() { DoctorRegistry = append(DoctorRegistry, calendarDoctorChecks...) }

// checkCalendarGrants reads the bridge's record of the grants
// (google/calendar_health.json) and warns about each configured brain
// whose grant broke.
func checkCalendarGrants(_ context.Context, env *DoctorEnv) DoctorResult {
	if env.Config == nil {
		return noConfig
	}
	data, err := env.ReadFile(env.Config.Boot.Layout().CalendarHealth())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return DoctorResult{Status: DoctorOK, Detail: "no brain's calendar has been used yet"}
	case err != nil:
		return DoctorResult{Status: DoctorWarn, Detail: "cannot read the calendar health file: " + err.Error()}
	}
	grants, err := google.ReadGrantHealth(data)
	if err != nil {
		return DoctorResult{Status: DoctorWarn, Detail: "the calendar health file is unreadable: " + err.Error()}
	}
	configured := map[string]bool{}
	for _, ref := range env.Config.BrainDirs {
		if ref.Approved != nil {
			configured[ref.Approved.ID] = true
		}
	}
	var broken []string
	for id, h := range grants {
		if configured[id] && h.Broken() {
			broken = append(broken, id+" ("+h.Detail+")")
		}
	}
	if len(broken) == 0 {
		return DoctorResult{Status: DoctorOK, Detail: "no brain's calendar sign-in is broken"}
	}
	slices.Sort(broken)
	return DoctorResult{Status: DoctorWarn,
		Detail: "Google Calendar does not work for " + strings.Join(broken, "; "),
		Fix:    "Dashboard → Brains → the brain → Calendar → Reconnect (docs/CALENDAR.md, Calendar overview)"}
}
