package errand

import (
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
}

// calendarDoc is Calendar in the document.
type calendarDoc struct {
	May        string   `json:"may"`
	FreeTimes  []string `json:"free_times"`
	MaxMinutes int      `json:"max_minutes"`
	Note       string   `json:"note,omitempty"`
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
		open := Span{Start: day.Add(time.Duration(h.From) * time.Minute), End: day.Add(time.Duration(h.To) * time.Minute)}
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
