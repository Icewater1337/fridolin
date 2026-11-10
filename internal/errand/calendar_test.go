package errand

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestNarrowest(t *testing.T) {
	for _, tc := range []struct {
		modes []string
		want  string
	}{
		{nil, CalendarOff},
		{[]string{"", ""}, CalendarOff},
		{[]string{CalendarBook}, CalendarBook},
		{[]string{CalendarBook, "", CalendarAsk}, CalendarAsk},
		{[]string{CalendarAsk, CalendarOffer, CalendarBook}, CalendarOffer},
		{[]string{CalendarBook, CalendarOff}, CalendarOff},
		{[]string{"maybe", CalendarBook}, CalendarBook},
	} {
		if got := Narrowest(tc.modes...); got != tc.want {
			t.Errorf("Narrowest(%q) = %q, want %q", tc.modes, got, tc.want)
		}
	}
	if MayBook(CalendarOffer) || !MayBook(CalendarAsk) || !MayBook(CalendarBook) {
		t.Error("MayBook")
	}
}

// FreeTimes keeps to the hours and days, leaves out what is busy and
// what is shorter than MinFree, and starts no earlier than from.
func TestFreeTimes(t *testing.T) {
	zurich, _ := time.LoadLocation("Europe/Zurich")
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 11, day, hour, minute, 0, 0, zurich) }
	h := Hours{Days: []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}, From: 9 * 60, To: 18 * 60, Location: zurich}
	busy := []Span{
		{at(4, 10, 0), at(4, 11, 0)},
		{at(4, 10, 30), at(4, 12, 0)}, // overlaps the one before
		{at(4, 12, 10), at(4, 17, 0)}, // leaves 12:00–12:10, too short
		{at(5, 8, 0), at(5, 19, 0)},   // all of Thursday
	}
	// Wednesday 4 Nov 9:30, to Monday 9 Nov 12:00: Saturday and Sunday are no days.
	got := FreeTimes(busy, at(4, 9, 30), at(9, 12, 0), h)
	want := []string{
		"Wed 2026-11-04 09:30–10:00",
		"Wed 2026-11-04 17:00–18:00",
		"Fri 2026-11-06 09:00–18:00",
		"Mon 2026-11-09 09:00–12:00",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d free times: %v", len(got), got)
	}
	for i, s := range got {
		if f := FormatSpan(s, zurich); f != want[i] {
			t.Errorf("free time %d = %s, want %s", i, f, want[i])
		}
	}
	if !Within(Span{at(6, 14, 0), at(6, 15, 30)}, got) || Within(Span{at(4, 9, 45), at(4, 10, 15)}, got) || Within(Span{at(5, 9, 0), at(5, 10, 0)}, got) {
		t.Error("Within")
	}
	if n := len(FreeTimes(nil, at(2, 0, 0), at(2, 0, 0).AddDate(1, 0, 0), h)); n != MaxFree {
		t.Errorf("a year of free days gave %d free times, want %d", n, MaxFree)
	}
	if s := FormatSpan(Span{at(4, 23, 0), at(5, 1, 0)}, zurich); s != "Wed 2026-11-04 23:00–Thu 2026-11-05 01:00" {
		t.Errorf("over midnight: %s", s)
	}
}

// FreeTimes keeps the hours in wall-clock time on the days DST changes (#641).
func TestFreeTimesAcrossDST(t *testing.T) {
	zurich, _ := time.LoadLocation("Europe/Zurich")
	sunday := Hours{Days: []time.Weekday{time.Sunday}, From: 8 * 60, To: 20 * 60, Location: zurich}
	for _, tc := range []struct {
		month, day int
		to         int
		want       string
	}{
		{3, 29, 20 * 60, "Sun 2026-03-29 08:00–20:00"},  // spring forward
		{10, 25, 20 * 60, "Sun 2026-10-25 08:00–20:00"}, // fall back
		{11, 8, 20 * 60, "Sun 2026-11-08 08:00–20:00"},  // an ordinary Sunday
		{3, 29, 24 * 60, "Sun 2026-03-29 08:00–Mon 2026-03-30 00:00"},
	} {
		h := sunday
		h.To = tc.to
		from := time.Date(2026, time.Month(tc.month), tc.day, 0, 0, 0, 0, zurich)
		got := FreeTimes(nil, from, from.AddDate(0, 0, 2), h)
		if len(got) != 1 || FormatSpan(got[0], zurich) != tc.want {
			t.Errorf("%d-%d to %d: got %v, want %s", tc.month, tc.day, tc.to, got, tc.want)
		}
	}
}

func TestParseBookStart(t *testing.T) {
	zurich, _ := time.LoadLocation("Europe/Zurich")
	want := time.Date(2026, 11, 6, 14, 30, 0, 0, zurich)
	for _, s := range []string{"2026-11-06 14:30", " 2026-11-06T14:30 ", "2026-11-06 14:30:00", "2026-11-06T13:30:00Z"} {
		if got, err := ParseBookStart(s, zurich); err != nil || !got.Equal(want) {
			t.Errorf("ParseBookStart(%q) = %v, %v", s, got, err)
		}
	}
	for _, s := range []string{"", "Friday at 2", "2026-11-06"} {
		if _, err := ParseBookStart(s, zurich); err == nil {
			t.Errorf("ParseBookStart(%q) took it", s)
		}
	}
}

// A book answer needs a time and minutes; other answers lose them.
func TestParseAnswerBook(t *testing.T) {
	a, err := ParseAnswer(`{"action":"book","text":"Done, see you then!","book_start":" 2026-11-06 14:30 ","book_minutes":60}`)
	if err != nil || a.BookStart != "2026-11-06 14:30" || a.BookMinutes != 60 || a.Text != "Done, see you then!" {
		t.Errorf("book: %+v %v", a, err)
	}
	for _, out := range []string{`{"action":"book","book_minutes":60}`, `{"action":"book","book_start":"2026-11-06 14:30"}`, `{"action":"book","book_start":"2026-11-06 14:30","book_minutes":2000}`} {
		if _, err := ParseAnswer(out); !errors.Is(err, ErrNoAnswer) {
			t.Errorf("%s: %v", out, err)
		}
	}
	if a, _ := ParseAnswer(`{"action":"reply","text":"hi","book_start":"2026-11-06 14:30","book_minutes":60}`); a.BookStart != "" || a.BookMinutes != 0 {
		t.Errorf("a reply kept a booking: %+v", a)
	}
}

func TestDocumentCarriesTheCalendar(t *testing.T) {
	var d document
	if err := json.Unmarshal([]byte(Document(Errand{})), &d); err != nil || d.Calendar != nil {
		t.Errorf("no calendar: %+v %v", d.Calendar, err)
	}
	e := Errand{Calendar: &Calendar{May: CalendarAsk, Free: []string{"Fri 2026-11-06 09:00–18:00"}, MaxMinutes: 90}}
	if err := json.Unmarshal([]byte(Document(e)), &d); err != nil || d.Calendar == nil || d.Calendar.May != CalendarAsk || len(d.Calendar.FreeTimes) != 1 || d.Calendar.MaxMinutes != 90 {
		t.Errorf("a calendar: %+v %v", d.Calendar, err)
	}
	if err := json.Unmarshal([]byte(Document(Errand{Calendar: &Calendar{May: CalendarOffer, Note: "no free times"}})), &d); err != nil || d.Calendar.FreeTimes == nil || d.Calendar.Note != "no free times" {
		t.Errorf("no free times: %+v %v", d.Calendar, err)
	}
}
