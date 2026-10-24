package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// reminderSourceBridge is the Source of a reminder the bridge keeps, a
// standing intent.
const reminderSourceBridge = "bridge"

// brainReminderFiles are the files a brain keeps its own reminders in,
// relative to its folder (docs/BRAINS.md, "Reminders"): .brain's, and
// skills/reminders.json, where a brain's remind skill keeps them, firing
// them itself. The bridge only lists them.
var brainReminderFiles = []string{".brain/reminders.json", "skills/reminders.json"}

// fileReminder is one entry of a reminders file: a list of these.
type fileReminder struct {
	ID json.RawMessage `json:"id"`
	// FireAt is when it fires next, in the bridge's time zone unless it
	// says another: 2026-10-24T20:00:00.
	FireAt  string `json:"fire_at"`
	Message string `json:"message"`
	// Sent marks one that fired and does not repeat (a repeating one is
	// set again for its next time).
	Sent            bool   `json:"sent"`
	RepeatEveryDays int    `json:"repeat_every_days"`
	JID             string `json:"jid"`
	CreatedAt       string `json:"created_at"`
	// Callback marks one that starts a run of the brain rather than
	// sending its message.
	Callback bool `json:"fridolin_callback"`
}

// brainFileReminders are the reminders still to come in brain id's
// reminders files in dir, read as the bridge reads a brain's files. A
// file that is not there, or not a list of reminders, has none.
func brainFileReminders(id, dir string, loc *time.Location) []reminderView {
	var out []reminderView
	for _, rel := range brainReminderFiles {
		data, err := fsx.ReadBrainFile(dir, rel)
		if err != nil {
			continue
		}
		var list []fileReminder
		if json.Unmarshal(data, &list) != nil {
			continue
		}
		for _, r := range list {
			if r.Sent || strings.TrimSpace(r.Message) == "" {
				continue
			}
			rv := reminderView{Brain: id, ID: rel + "#" + strings.Trim(string(r.ID), `"`), Text: r.Message, Chat: r.JID, Source: rel}
			next, ok := parseLocal(r.FireAt, loc)
			if ok {
				n := next
				rv.Next = &n
			}
			switch {
			case r.RepeatEveryDays == 1:
				rv.When = "daily " + next.Format("15:04")
			case r.RepeatEveryDays > 1:
				rv.When = fmt.Sprintf("every %d days at %s", r.RepeatEveryDays, next.Format("15:04"))
			case ok:
				rv.When = next.Format("2006-01-02 15:04")
			default:
				rv.When = r.FireAt
			}
			if r.Callback {
				rv.When += " (a run of the brain)"
			}
			if c, ok := parseLocal(r.CreatedAt, loc); ok {
				rv.Created = c
			}
			out = append(out, rv)
		}
	}
	return out
}

// parseLocal reads a time as a brain's file writes it: RFC 3339, or
// without its zone, in loc.
func parseLocal(s string, loc *time.Location) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
