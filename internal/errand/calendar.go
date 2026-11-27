package errand

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// An errand's run may use the owner's calendar only through the bridge
// (docs/BRAINS.md, "Calendar in errands"): it is handed the free times
// the bridge worked out from the calendar's busy times, never an event,
// and it can only ask to book one of them, which the bridge checks
// before it books, or asks the owner.

// How far an errand may use the owner's calendar, from least to most.
const (
	// CalendarOff: not at all.
	CalendarOff = "off"
	// CalendarOffer: the run is handed the free times, to offer them.
	CalendarOffer = "offer"
	// CalendarAsk: it may ask to book one, which the owner confirms in a
	// poll first.
	CalendarAsk = "book_after_yes"
	// CalendarBook: it may book one.
	CalendarBook = "book"
)

// CalendarModes are the modes, least first.
var CalendarModes = []string{CalendarOff, CalendarOffer, CalendarAsk, CalendarBook}

// Narrowest is the least of modes, of which "" names no limit; all ""
// is CalendarOff.
func Narrowest(modes ...string) string {
	least := len(CalendarModes)
	for _, m := range modes {
		if m == "" {
			continue
		}
		if i := slices.Index(CalendarModes, m); i >= 0 && i < least {
			least = i
		}
	}
	if least == len(CalendarModes) {
		return CalendarOff
	}
	return CalendarModes[least]
}

// MayBook reports whether mode lets a run ask to book.
func MayBook(mode string) bool { return mode == CalendarAsk || mode == CalendarBook }

// Calendar is what a run is told of the owner's calendar.
type Calendar struct {
	// May is CalendarOffer, CalendarAsk or CalendarBook.
	May string
	// Free are the owner's free times, as FormatSpan writes them.
	Free []string
	// MaxMinutes is the longest a booking may be.
	MaxMinutes int
	// Note says what else the run must know: why there are no free times,
	// or that the conversation has its booking.
	Note string
	// Window is what the owner limited this errand's times to, nil for
	// nothing beyond the calendar's own hours.
	Window *Window
	// Meetings is set for a conversation the person began: the rules for
	// in-person and remote meetings (Instructions, rule 14).
	Meetings string
}

// calendarDoc is Calendar in the document.
type calendarDoc struct {
	May        string   `json:"may"`
	FreeTimes  []string `json:"free_times"`
	MaxMinutes int      `json:"max_minutes"`
	Note       string   `json:"note,omitempty"`
	Window     *Window  `json:"window,omitempty"`
	Meetings   string   `json:"meeting_rules,omitempty"`
}

// Span is a stretch of time.
type Span struct{ Start, End time.Time }

// Hours are the times the owner may be booked: on Days, From to To
// (minutes after midnight), in Location.
type Hours struct {
	Days     []time.Weekday
	From, To int
	Location *time.Location
}

// MinFree is the shortest free time a run is handed.
const MinFree = 30 * time.Minute

// MaxFree is how many free times a run is handed at most, the earliest.
const MaxFree = 30

// FreeTimes are the times between from and to within h that busy leaves
// free, of MinFree or more, the earliest MaxFree.
func FreeTimes(busy []Span, from, to time.Time, h Hours) []Span {
	busy = merge(busy)
	var out []Span
	loc := h.Location
	if loc == nil {
		loc = time.UTC
	}
	day := time.Date(from.In(loc).Year(), from.In(loc).Month(), from.In(loc).Day(), 0, 0, 0, 0, loc)
	for ; day.Before(to) && len(out) < MaxFree; day = day.AddDate(0, 0, 1) {
		if !slices.Contains(h.Days, day.Weekday()) {
			continue
		}
		// time.Date keeps wall-clock time across a DST change; Add would not.
		y, m, d := day.Date()
		open := Span{Start: time.Date(y, m, d, 0, h.From, 0, 0, loc), End: time.Date(y, m, d, 0, h.To, 0, 0, loc)}
		if open.Start.Before(from) {
			open.Start = from
		}
		if open.End.After(to) {
			open.End = to
		}
		for _, s := range subtract(open, busy) {
			if s.End.Sub(s.Start) >= MinFree && len(out) < MaxFree {
				out = append(out, s)
			}
		}
	}
	return out
}

// BlockedSpans are the stretches from..to that lie on days between
// fromMin and toMin (minutes after midnight), in loc: the hours that are
// never free for an in-person meeting.
func BlockedSpans(from, to time.Time, days []time.Weekday, fromMin, toMin int, loc *time.Location) []Span {
	var out []Span
	f := from.In(loc)
	for day := time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, loc); day.Before(to); day = day.AddDate(0, 0, 1) {
		if slices.Contains(days, day.Weekday()) {
			y, m, d := day.Date()
			out = append(out, Span{Start: time.Date(y, m, d, 0, fromMin, 0, 0, loc), End: time.Date(y, m, d, 0, toMin, 0, 0, loc)})
		}
	}
	return out
}

// Overlaps reports whether s shares any time with one of spans.
func Overlaps(s Span, spans []Span) bool {
	return slices.ContainsFunc(spans, func(o Span) bool { return s.Start.Before(o.End) && o.Start.Before(s.End) })
}

// merge sorts spans and joins those that touch or overlap.
func merge(spans []Span) []Span {
	s := slices.Clone(spans)
	slices.SortFunc(s, func(a, b Span) int { return a.Start.Compare(b.Start) })
	var out []Span
	for _, x := range s {
		if !x.End.After(x.Start) {
			continue
		}
		if n := len(out); n > 0 && !x.Start.After(out[n-1].End) {
			if x.End.After(out[n-1].End) {
				out[n-1].End = x.End
			}
			continue
		}
		out = append(out, x)
	}
	return out
}

// subtract is what of open busy, merged, leaves.
func subtract(open Span, busy []Span) []Span {
	var out []Span
	at := open.Start
	for _, b := range busy {
		if !b.End.After(at) {
			continue
		}
		if !b.Start.Before(open.End) {
			break
		}
		if b.Start.After(at) {
			out = append(out, Span{Start: at, End: b.Start})
		}
		at = b.End
	}
	if open.End.After(at) {
		out = append(out, Span{Start: at, End: open.End})
	}
	return out
}

// Within reports whether s lies inside one of free.
func Within(s Span, free []Span) bool {
	return slices.ContainsFunc(free, func(f Span) bool { return !s.Start.Before(f.Start) && !s.End.After(f.End) })
}

// FormatSpan writes s for a run and for the owner, in loc: "Tue
// 2026-11-05 14:00–18:00", or with both days when it spans midnight.
func FormatSpan(s Span, loc *time.Location) string {
	start, end := s.Start.In(loc), s.End.In(loc)
	if start.YearDay() == end.YearDay() && start.Year() == end.Year() {
		return start.Format("Mon 2006-01-02 15:04") + "–" + end.Format("15:04")
	}
	return start.Format("Mon 2006-01-02 15:04") + "–" + end.Format("Mon 2006-01-02 15:04")
}

// ParseBookStart reads a run's book_start, a local time in loc:
// "2006-01-02 15:04", with a T, seconds or an offset allowed.
func ParseBookStart(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(loc), nil
	}
	return time.Time{}, fmt.Errorf("book_start %q is no time as YYYY-MM-DD HH:MM", s)
}

// Window is what the owner asked of one errand's times, as contact_person
// takes it: the bridge offers and books only times inside it.
type Window struct {
	// Days are weekdays as "mon".."sun"; none means any.
	Days []string `json:"days,omitempty"`
	// From and To are "HH:MM" local; a time lies inside when it starts at
	// From or later and ends at To or earlier. Empty means no limit.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Minutes is how long the meeting lasts; 0 means DefaultMinutes.
	Minutes int `json:"minutes,omitempty"`
	// Place is where: an address, or "online".
	Place string `json:"place,omitempty"`
}

// DefaultMinutes is a meeting's length when the window names none.
const DefaultMinutes = 60

var weekdayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func clock(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	return t.Hour()*60 + t.Minute(), err
}

// Validate reports why w cannot be used, and lower-cases its days.
func (w *Window) Validate() error {
	for i, d := range w.Days {
		d = strings.ToLower(strings.TrimSpace(d))
		if len(d) > 3 {
			d = d[:3]
		}
		if !slices.Contains(weekdayNames, d) {
			return fmt.Errorf("window day %q is no weekday (mon..sun)", w.Days[i])
		}
		w.Days[i] = d
	}
	var from, to int
	var err error
	if w.From != "" {
		if from, err = clock(w.From); err != nil {
			return fmt.Errorf("window from %q is no HH:MM", w.From)
		}
	}
	to = 24 * 60
	if w.To != "" {
		if to, err = clock(w.To); err != nil {
			return fmt.Errorf("window to %q is no HH:MM", w.To)
		}
	}
	if to <= from || w.Minutes < 0 || w.Minutes > 24*60 {
		return errors.New("window needs from before to, and minutes of at most 24 hours")
	}
	w.Place = strings.TrimSpace(w.Place)
	return nil
}

// Length is the meeting's length.
func (w *Window) Length() time.Duration {
	if w == nil || w.Minutes == 0 {
		return DefaultMinutes * time.Minute
	}
	return time.Duration(w.Minutes) * time.Minute
}

// Allows reports whether s lies inside w, in loc. A nil window allows all.
func (w *Window) Allows(s Span, loc *time.Location) bool {
	if w == nil {
		return true
	}
	start, end := s.Start.In(loc), s.End.In(loc)
	if len(w.Days) > 0 && !slices.Contains(w.Days, weekdayNames[start.Weekday()]) {
		return false
	}
	if start.YearDay() != end.YearDay() || start.Year() != end.Year() {
		return false
	}
	if from, err := clock(w.From); w.From != "" && err == nil && start.Hour()*60+start.Minute() < from {
		return false
	}
	if to, err := clock(w.To); w.To != "" && err == nil && end.Hour()*60+end.Minute() > to {
		return false
	}
	return true
}
