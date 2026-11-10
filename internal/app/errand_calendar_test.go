package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/errand"
	"github.com/Icewater1337/fridolin/internal/google"
	"github.com/Icewater1337/fridolin/internal/manifest"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
)

// everyDay are errands.calendar's days for tests that must not depend on
// the weekday they run on.
var everyDay = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// calendarErrandHarness is errandHarness whose core brain has a Google
// calendar (the fake one's you@example.com) and errands.calendar cal.
func calendarErrandHarness(t *testing.T, cal manifest.ErrandCalendar) (*harness, *errandRuns, *fakeCalendarAPI) {
	t.Helper()
	runs := &errandRuns{}
	optionsOf(t).hooks.errandRun = runs.run
	optionsOf(t).waits.errandGather = 10 * time.Millisecond
	h := coreHarness(t, nil)
	f := newFakeCalendarAPI(t)
	h.b.google = google.New(googleTestOptions(googleOptionsFor(h.b.cfgs), f.srv))
	if w := h.Do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, f.srv.URL)}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	tok := google.Token{Token: "ACCESS-OK", RefreshToken: "RT-1", ClientID: "123-abc.apps.googleusercontent.com", Scopes: calendarScopes, Expiry: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
	data, _ := json.Marshal(tok)
	if err := os.WriteFile(h.b.google.TokenPath("core"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if w := h.Do("PUT", "/api/brains/core/calendar", map[string]any{"account": "you@example.com", "calendars": []string{"you@example.com"}, "default": "you@example.com"}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	m := h.Approved()["core"]
	m.Errands = &manifest.Errands{OwnerName: "Sam", Calendar: &cal}
	if w := h.Do("PUT", "/api/brains/core", map[string]any{"manifest": m}); w.Code != http.StatusOK {
		t.Fatalf("turning errands on: %d %s", w.Code, w.Body.String())
	}
	h.knowAlex()
	return h, runs, f
}

// inTwoDays is hour o'clock the day after tomorrow, in the owner's time
// zone, which the errands' free times are in.
func (h *harness) inTwoDays(hour int) time.Time {
	loc := RuntimeConfig(h.b.cfgs).Location()
	d := time.Now().In(loc).AddDate(0, 0, 2)
	return time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, loc)
}

// bookAnswer is a run's answer that books start for an hour.
func bookAnswer(start time.Time, text string) string {
	return fmt.Sprintf(`{"action":"book","text":%q,"book_start":%q,"book_minutes":60}`, text, start.Format("2006-01-02 15:04"))
}

// TestErrandBooksInTheCalendar: with errands.calendar book, the run is
// handed the free times, never an event; a time it asks to book that is
// not free goes back to it, and the next one is booked, as an event that
// names the person and the errand only; the owner hears of it. A second
// booking is refused, and after a second refusal the owner is asked.
func TestErrandBooksInTheCalendar(t *testing.T) {
	t.Parallel()
	h, runs, f := calendarErrandHarness(t, manifest.ErrandCalendar{Mode: errand.CalendarBook, Days: everyDay, DaysAhead: 5})
	ctx := context.Background()
	loc := RuntimeConfig(h.b.cfgs).Location()
	busyAt, freeAt := h.inTwoDays(10), h.inTwoDays(14)
	f.mu.Lock()
	f.busy["you@example.com"] = []google.BusySlot{{Start: busyAt.Format(time.RFC3339), End: busyAt.Add(2 * time.Hour).Format(time.RFC3339)}}
	f.events["you@example.com"] = []map[string]any{calendarEvent("x", "Therapy", busyAt.Format(time.RFC3339), busyAt.Add(2*time.Hour).Format(time.RFC3339))}
	f.mu.Unlock()
	if _, err := h.b.openErrand(ctx, h.ownerRun(), dinner); err != nil {
		t.Fatal(err)
	}
	e := h.onlyErrand()
	runs.say(bookAnswer(busyAt, "Booked for 10!"), bookAnswer(freeAt, "Great, 14:00 it is!"))
	h.theyWrite("The day after tomorrow works, any time")
	h.WaitSent(errandJID, "Great, 14:00 it is!")

	handed := runs.handed()
	cal := handed[0].Calendar
	afternoon := errand.FormatSpan(errand.Span{Start: busyAt.Add(2 * time.Hour), End: h.inTwoDays(20)}, loc)
	if cal == nil || cal.May != errand.CalendarBook || cal.MaxMinutes != 90 || !slices.Contains(cal.Free, afternoon) {
		t.Fatalf("the run's calendar: %+v, want %s among the free times", cal, afternoon)
	}
	for _, d := range handed {
		if b, _ := json.Marshal(d); strings.Contains(string(b), "Therapy") {
			t.Error("a run was shown an event")
		}
	}
	busyWhen := errand.FormatSpan(errand.Span{Start: busyAt, End: busyAt.Add(time.Hour)}, loc)
	if l := handed[1].Lines[len(handed[1].Lines)-1]; l.From != errand.FromCalendar || l.Text != "Not booked: "+busyWhen+" is not one of the free times." {
		t.Errorf("the second run's last line: %+v", l)
	}
	for _, text := range h.SentTo(errandJID) {
		if strings.Contains(text, "Booked for 10") {
			t.Errorf("the text of a booking that was not made went out: %q", text)
		}
	}
	when := errand.FormatSpan(errand.Span{Start: freeAt, End: freeAt.Add(time.Hour)}, loc)
	h.WaitSent(harnessDM, outboundmsg.ErrandBookedNotice("Alex", e.ID, when))
	f.mu.Lock()
	inserted := slices.Clone(f.inserted)
	f.mu.Unlock()
	if len(inserted) != 1 {
		t.Fatalf("%d events made", len(inserted))
	}
	ev, _ := json.Marshal(inserted[0])
	if inserted[0]["summary"] != "Alex (arranged by Fridolin)" || !strings.Contains(string(ev), freeAt.Format("2006-01-02T15:04")) ||
		!strings.Contains(string(ev), e.ID) || strings.Contains(string(ev), "any time") {
		t.Errorf("the event: %s", ev)
	}
	if got := h.errandOf(e.ID); got.Booked != store.BookingEvent+"new1" || !got.BookedFrom.Equal(freeAt) || !got.BookedTo.Equal(freeAt.Add(time.Hour)) {
		t.Errorf("the errand's booking: %+v", got)
	}

	// One booking an errand: a second is refused twice, and the owner asked.
	runs.say(bookAnswer(freeAt.Add(2*time.Hour), "Done!"), bookAnswer(freeAt.Add(3*time.Hour), "Done!"))
	h.theyWrite("Could we also meet later that day?")
	h.WaitSent(errandJID, outboundmsg.ErrandBookingCheckMessage("Sam"))
	h.WaitSent(harnessDM, "could not be booked")
	f.mu.Lock()
	n := len(f.inserted)
	f.mu.Unlock()
	if n != 1 {
		t.Errorf("%d events after a second booking", n)
	}
	if last := runs.handed()[len(runs.handed())-1]; last.Calendar == nil || last.Calendar.May != errand.CalendarOffer || !strings.Contains(last.Calendar.Note, "has its booking") {
		t.Errorf("the calendar after the booking: %+v", last.Calendar)
	}
}

// TestErrandBookingWaitsForTheOwner: with book_after_yes the run's
// text goes out at once, the owner gets a poll, and only their yes books
// it; the run then hears of it and tells the person. A no books nothing.
func TestErrandBookingWaitsForTheOwner(t *testing.T) {
	t.Parallel()
	h, runs, f := calendarErrandHarness(t, manifest.ErrandCalendar{Mode: errand.CalendarAsk, Days: everyDay, DaysAhead: 5})
	ctx := context.Background()
	loc := RuntimeConfig(h.b.cfgs).Location()
	at := h.inTwoDays(14)
	when := errand.FormatSpan(errand.Span{Start: at, End: at.Add(time.Hour)}, loc)
	if _, err := h.b.openErrand(ctx, h.ownerRun(), dinner); err != nil {
		t.Fatal(err)
	}
	e := h.onlyErrand()
	runs.say(bookAnswer(at, "Let me check with Sam!"))
	h.theyWrite("14:00 the day after tomorrow?")
	h.WaitSent(errandJID, "Let me check with Sam!")
	h.WaitSent(harnessDM, outboundmsg.ErrandBookingDraft("Alex", e.ID, when))
	poll := h.waitPoll(harnessDM, "Book "+when+" with Alex")
	if got := h.errandOf(e.ID); !strings.HasPrefix(got.Booked, store.BookingAsking) {
		t.Errorf("waiting for the owner: %+v", got)
	}
	f.mu.Lock()
	n := len(f.inserted)
	f.mu.Unlock()
	if n != 0 {
		t.Fatal("booked before the owner's yes")
	}

	runs.say(`{"action":"reply","text":"Sam confirmed, see you then!"}`)
	h.vote(poll.ID, "15551234567", outboundmsg.ApprovalYes)
	h.WaitSent(errandJID, "Sam confirmed, see you then!")
	h.WaitSent(harnessDM, outboundmsg.ErrandBookedNotice("Alex", e.ID, when))
	if got := h.errandOf(e.ID); got.Booked != store.BookingEvent+"new1" {
		t.Errorf("after the yes: %+v", got)
	}
	handed := runs.handed()
	if l := handed[len(handed)-1].Lines; l[len(l)-1] != (errand.Line{From: errand.FromCalendar, Text: "Sam confirmed: " + when + " is booked in their calendar. Tell them.", At: l[len(l)-1].At}) {
		t.Errorf("what the run heard: %+v", l[len(l)-1])
	}

	// Another errand, and a no.
	runs.say(`{"action":"done","text":"Bye!","summary":"Booked."}`)
	h.theyWrite("Great")
	h.WaitFor("the end of the first errand", func() bool { return h.errandOf(e.ID).State == store.ErrandDone })
	if _, err := h.b.openErrand(ctx, h.ownerRun(), dinner); err != nil {
		t.Fatal(err)
	}
	list, _ := h.b.store.RecentErrands(ctx, "core", 10)
	second := list[0]
	later := at.Add(3 * time.Hour)
	laterWhen := errand.FormatSpan(errand.Span{Start: later, End: later.Add(time.Hour)}, loc)
	runs.say(bookAnswer(later, "I'll ask Sam."), `{"action":"reply","text":"Sorry, Sam can't make it then."}`)
	h.theyWrite("And 17:00?")
	no := h.waitPoll(harnessDM, "Book "+laterWhen+" with Alex")
	h.vote(no.ID, "15551234567", outboundmsg.ApprovalNo)
	h.WaitSent(errandJID, "Sorry, Sam can't make it then.")
	if got := h.errandOf(second.ID); got.Booked != "" {
		t.Errorf("after a no: %+v", got)
	}
	f.mu.Lock()
	n = len(f.inserted)
	f.mu.Unlock()
	if n != 1 {
		t.Errorf("%d events after a no", n)
	}
}

// TestErrandCalendarNarrows: what brain.json allows, a person and an
// errand may narrow, never widen.
func TestErrandCalendarNarrows(t *testing.T) {
	t.Parallel()
	h, runs, _ := calendarErrandHarness(t, manifest.ErrandCalendar{Mode: errand.CalendarAsk, Days: everyDay})
	ctx := context.Background()
	wide := dinner
	wide.Calendar = errand.CalendarBook
	if _, err := h.b.openErrand(ctx, h.ownerRun(), wide); err != nil {
		t.Fatal(err)
	}
	runs.say(`{"action":"done","text":"Bye","summary":"s"}`)
	h.theyWrite("hi")
	h.WaitFor("the first run", func() bool { return len(runs.handed()) == 1 })
	if c := runs.handed()[0].Calendar; c == nil || c.May != errand.CalendarAsk {
		t.Errorf("an errand asking for more than brain.json allows: %+v", c)
	}
	h.WaitFor("the end of it", func() bool {
		list, _ := h.b.store.RecentErrands(ctx, "core", 10)
		return list[0].State == store.ErrandDone
	})

	if _, err := h.b.store.SetErrandContactWays(ctx, errandNumber, store.ContactConverse, "", errand.CalendarOffer, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.b.openErrand(ctx, h.ownerRun(), wide); err != nil {
		t.Fatal(err)
	}
	runs.say(`{"action":"done","text":"Bye","summary":"s"}`)
	h.theyWrite("hi again")
	h.WaitFor("the second run", func() bool { return len(runs.handed()) == 2 })
	if c := runs.handed()[1].Calendar; c == nil || c.May != errand.CalendarOffer {
		t.Errorf("someone allowed only the free times: %+v", c)
	}
	h.WaitFor("the end of it", func() bool {
		list, _ := h.b.store.RecentErrands(ctx, "core", 10)
		return list[0].State == store.ErrandDone
	})

	off := dinner
	off.Calendar = errand.CalendarOff
	if _, err := h.b.openErrand(ctx, h.ownerRun(), off); err != nil {
		t.Fatal(err)
	}
	runs.say(`{"action":"done","text":"Bye","summary":"s"}`)
	h.theyWrite("hi once more")
	h.WaitFor("the third run", func() bool { return len(runs.handed()) == 3 })
	if c := runs.handed()[2].Calendar; c != nil {
		t.Errorf("an errand with the calendar off: %+v", c)
	}
	bad := dinner
	bad.Calendar = "always"
	if _, err := h.b.openErrand(ctx, h.ownerRun(), bad); err == nil || !strings.Contains(err.Error(), "calendar is off, offer") {
		t.Errorf("an unknown calendar mode: %v", err)
	}
}

// TestErrandBookingYesWithUnreadableCalendar: a yes on the poll books
// nothing when the calendar cannot be read then; the run hears why.
func TestErrandBookingYesWithUnreadableCalendar(t *testing.T) {
	t.Parallel()
	h, runs, f := calendarErrandHarness(t, manifest.ErrandCalendar{Mode: errand.CalendarAsk, Days: everyDay, DaysAhead: 5})
	ctx := context.Background()
	loc := RuntimeConfig(h.b.cfgs).Location()
	at := h.inTwoDays(14)
	when := errand.FormatSpan(errand.Span{Start: at, End: at.Add(time.Hour)}, loc)
	if _, err := h.b.openErrand(ctx, h.ownerRun(), dinner); err != nil {
		t.Fatal(err)
	}
	e := h.onlyErrand()
	runs.say(bookAnswer(at, "Let me check with Sam!"), `{"action":"reply","text":"Sam will come back to you."}`)
	h.theyWrite("14:00 the day after tomorrow?")
	poll := h.waitPoll(harnessDM, "Book "+when+" with Alex")
	f.mu.Lock()
	f.broken["you@example.com"] = true
	f.mu.Unlock()
	h.vote(poll.ID, "15551234567", outboundmsg.ApprovalYes)
	h.WaitSent(errandJID, "Sam will come back to you.")
	if got := h.errandOf(e.ID); got.Booked != "" {
		t.Errorf("after a yes it could not check: %+v", got)
	}
	f.mu.Lock()
	n := len(f.inserted)
	f.mu.Unlock()
	if n != 0 {
		t.Errorf("%d events booked unchecked", n)
	}
	handed := runs.handed()
	if l := handed[len(handed)-1].Lines; !strings.Contains(l[len(l)-1].Text, "the calendar cannot be read just now: it is not booked") {
		t.Errorf("what the run heard: %+v", l[len(l)-1])
	}
}

// yesAfterTheEnd asks the owner to book, lets end end the errand, and
// then votes yes on the poll: nothing is booked, the booking is cleared,
// and the owner hears it was not booked because the errand had ended
// (#639).
func yesAfterTheEnd(t *testing.T, end func(h *harness, runs *errandRuns, e store.Errand)) {
	t.Helper()
	h, runs, f := calendarErrandHarness(t, manifest.ErrandCalendar{Mode: errand.CalendarAsk, Days: everyDay, DaysAhead: 5})
	loc := RuntimeConfig(h.b.cfgs).Location()
	at := h.inTwoDays(14)
	when := errand.FormatSpan(errand.Span{Start: at, End: at.Add(time.Hour)}, loc)
	if _, err := h.b.openErrand(context.Background(), h.ownerRun(), dinner); err != nil {
		t.Fatal(err)
	}
	e := h.onlyErrand()
	runs.say(bookAnswer(at, "Let me check with Sam!"))
	h.theyWrite("14:00 the day after tomorrow?")
	poll := h.waitPoll(harnessDM, "Book "+when+" with Alex")
	h.WaitSent(errandJID, "Let me check with Sam!")

	end(h, runs, e)
	h.WaitSent(harnessDM, outboundmsg.ErrandBookingEndedNotice("Alex", e.ID, when))
	h.vote(poll.ID, "15551234567", outboundmsg.ApprovalYes)
	h.WaitFor("the yes", func() bool { return len(h.auditOf(audit.ApprovalVote)) > 0 })
	if got := h.errandOf(e.ID); got.Booked != "" {
		t.Errorf("the booking after the end: %+v", got)
	}
	f.mu.Lock()
	n := len(f.inserted)
	f.mu.Unlock()
	if n != 0 {
		t.Errorf("%d events booked for an errand that had ended", n)
	}
}

// TestErrandBookingYesAfterStop: a yes after errand_stop books nothing.
func TestErrandBookingYesAfterStop(t *testing.T) {
	t.Parallel()
	yesAfterTheEnd(t, func(h *harness, _ *errandRuns, e store.Errand) {
		if err := h.b.stopErrand(context.Background(), "core", e.ID); err != nil {
			t.Fatal(err)
		}
	})
}

// TestErrandBookingYesAfterDone: a yes after the run ended the errand
// with done books nothing.
func TestErrandBookingYesAfterDone(t *testing.T) {
	t.Parallel()
	yesAfterTheEnd(t, func(h *harness, runs *errandRuns, e store.Errand) {
		runs.say(`{"action":"done","text":"Bye!","summary":"Asked Sam."}`)
		h.theyWrite("Thanks, bye")
		h.WaitFor("the end", func() bool { return h.errandOf(e.ID).State == store.ErrandDone })
	})
}
