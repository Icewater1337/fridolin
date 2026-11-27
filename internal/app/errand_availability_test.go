package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/errand"
	"github.com/Icewater1337/fridolin/internal/google"
	"github.com/Icewater1337/fridolin/internal/manifest"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
)

// meetingAnswer is a run's answer that books start for an hour, for kind.
func meetingAnswer(start time.Time, kind, text string) string {
	return fmt.Sprintf(`{"action":"book","text":%q,"book_start":%q,"book_minutes":60,"meeting":%q}`, text, start.Format("2006-01-02 15:04"), kind)
}

// availabilityHarness is a calendar harness whose owner is busy at 10:00
// two days from now with a "Therapy" event, and whose blocked in-person
// hours are every day's 08:00 to 17:00.
func availabilityHarness(t *testing.T) (*harness, *errandRuns, *fakeCalendarAPI) {
	t.Helper()
	h, runs, f := calendarErrandHarness(t, manifest.ErrandCalendar{Mode: errand.CalendarBook, Days: everyDay, DaysAhead: 5,
		InPersonBlocked: &manifest.ErrandBlocked{Days: everyDay}})
	busyAt := h.inTwoDays(10)
	f.mu.Lock()
	f.busy["you@example.com"] = []google.BusySlot{{Start: busyAt.Format(time.RFC3339), End: busyAt.Add(time.Hour).Format(time.RFC3339)}}
	f.events["you@example.com"] = []map[string]any{calendarEvent("x", "Therapy", busyAt.Format(time.RFC3339), busyAt.Add(time.Hour).Format(time.RFC3339))}
	f.mu.Unlock()
	return h, runs, f
}

func (f *fakeCalendarAPI) madeEvents() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inserted)
}

// TestAvailabilityInPerson: asked by the person who began the
// conversation, the run is handed free times only: never an event, and
// none inside the blocked in-person hours. A booking asked for inside them
// is refused, one outside is made.
func TestAvailabilityInPerson(t *testing.T) {
	t.Parallel()
	h, runs, f := availabilityHarness(t)
	loc := RuntimeConfig(h.b.cfgs).Location()
	runs.say(`{"action":"reply","text":"In person or remote?"}`,
		meetingAnswer(h.inTwoDays(14), "in_person", "Done at 14!"), meetingAnswer(h.inTwoDays(18), "in_person", "18:00 it is!"))
	h.theyWrite("When does Sam have time?")
	h.WaitSent(errandJID, "In person or remote?")
	h.theyWrite("In person, any time")
	h.WaitSent(errandJID, "18:00 it is!")

	handed := runs.handed()
	cal := handed[0].Calendar
	evening := errand.FormatSpan(errand.Span{Start: h.inTwoDays(17), End: h.inTwoDays(20)}, loc)
	if cal == nil || cal.Meetings == "" || !slices.Contains(cal.Free, evening) {
		t.Fatalf("the run's calendar: %+v, want %s among the free times", cal, evening)
	}
	for _, s := range cal.Free {
		if strings.Contains(s, "08:00–") || strings.Contains(s, " 10:00") || strings.Contains(s, " 14:00") {
			t.Errorf("free time %q lies in the blocked hours", s)
		}
	}
	for _, d := range handed {
		if b, _ := json.Marshal(d); strings.Contains(string(b), "Therapy") {
			t.Error("a run was shown an event")
		}
	}
	got := handed[len(handed)-1].Lines
	if l := got[len(got)-1]; l.From != errand.FromCalendar || !strings.Contains(l.Text, "blocked hours for in-person") {
		t.Errorf("the line after 14:00: %+v", l)
	}
	when := errand.FormatSpan(errand.Span{Start: h.inTwoDays(18), End: h.inTwoDays(19)}, loc)
	h.WaitSent(harnessDM, outboundmsg.ErrandBookedNotice("Alex", h.onlyErrand().ID, when))
	if n := f.madeEvents(); n != 1 {
		t.Errorf("%d events made", n)
	}
}

// TestAvailabilityRemoteAsksTheOwner: a remote meeting, or one whose kind
// the run did not say, is never booked on the run's own; after the owner's
// word in the conversation it may be, even inside the in-person hours.
func TestAvailabilityRemoteAsksTheOwner(t *testing.T) {
	t.Parallel()
	h, runs, f := availabilityHarness(t)
	ctx := context.Background()
	runs.say(meetingAnswer(h.inTwoDays(18), "remote", "Booked!"), meetingAnswer(h.inTwoDays(18), "", "Booked!"))
	h.theyWrite("A video call, day after tomorrow evening?")
	h.WaitSent(errandJID, outboundmsg.ErrandBookingCheckMessage("Sam"))
	for _, text := range h.SentTo(errandJID) {
		if strings.Contains(text, "Booked!") {
			t.Errorf("the text of a booking that was not made went out: %q", text)
		}
	}
	if n := f.madeEvents(); n != 0 {
		t.Fatalf("%d events made without the owner", n)
	}
	e := h.onlyErrand()
	if err := h.b.store.AddErrandLine(ctx, e.ID, store.ErrandLine{Sender: errand.FromOwner, Text: "Fine with me.", At: time.Now()}, false); err != nil {
		t.Fatal(err)
	}
	runs.say(meetingAnswer(h.inTwoDays(11), "remote", "Booked 11!"))
	h.theyWrite("Then 11?")
	h.WaitSent(errandJID, "Booked 11!")
	if n := f.madeEvents(); n != 1 {
		t.Errorf("%d events made after the owner agreed", n)
	}
}

// TestAvailabilityOnlyForConversationsTheyBegin: an errand the owner's
// brain opened is not held to the in-person hours.
func TestAvailabilityOnlyForConversationsTheyBegin(t *testing.T) {
	t.Parallel()
	h, runs, f := availabilityHarness(t)
	if _, err := h.b.openErrand(context.Background(), h.ownerRun(), dinner); err != nil {
		t.Fatal(err)
	}
	runs.say(bookAnswer(h.inTwoDays(14), "Great, 14:00!"))
	h.theyWrite("Anytime")
	h.WaitSent(errandJID, "Great, 14:00!")
	if n := f.madeEvents(); n != 1 {
		t.Errorf("%d events made", n)
	}
}
