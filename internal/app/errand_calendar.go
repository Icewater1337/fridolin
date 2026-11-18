package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/errand"
	"github.com/Icewater1337/fridolin/internal/google"
	"github.com/Icewater1337/fridolin/internal/manifest"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
)

// Errands and the owner's calendar (docs/BRAINS.md, "Calendar in
// errands"). An errand's run is handed the free times of its brain's
// calendar, worked out here from the busy times, never an event; and it
// can only ask to book one of them. The bridge books it only after it
// checked that the errand may, that it is one of the free times and
// still free, as long as allowed, and the errand's first booking; and
// with book_after_yes only once the owner said yes in a poll. The event
// it makes says who it is with, never what they wrote.

// errandBookingNotify is the Notify of the approvals of a booking: their
// answers come back to errandBookingDecided.
const errandBookingNotify = "fridolin:errand-booking"

// errandBookingLead is how soon a booking may start at the earliest.
const errandBookingLead = time.Hour

// errNotBooked is a booking the bridge did not make.
var errNotBooked = errors.New("not booked")

// errandBookingRequest is the Details of a booking's approval.
type errandBookingRequest struct {
	Errand string    `json:"errand"`
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
}

// errandCalendarOf is how far errand e may use the calendar of its
// brain, whose errands settings are s: the least of what brain.json,
// the person and the errand allow; and the brain's calendar settings.
func (b *App) errandCalendarOf(ctx context.Context, e store.Errand, s manifest.Errands) (string, manifest.ErrandCalendar) {
	if s.Calendar == nil {
		return errand.CalendarOff, manifest.ErrandCalendar{}
	}
	person := ""
	if c, err := b.store.ErrandContactOf(ctx, e.Contact); err == nil {
		person = c.Calendar
	}
	return errand.Narrowest(s.Calendar.Level(), person, e.Calendar), *s.Calendar
}

// googleZone is loc as Google takes a time zone, "" for the machine's.
func googleZone(loc *time.Location) string {
	if loc == nil || loc == time.Local || loc.String() == "Local" {
		return ""
	}
	return loc.String()
}

// errandFreeTimes are the free times of brain's calendar from now on,
// within c's hours, in loc.
func (b *App) errandFreeTimes(ctx context.Context, brain string, c manifest.ErrandCalendar, now time.Time, loc *time.Location) ([]errand.Span, error) {
	br := b.brainMgr.Get(brain)
	if br == nil {
		return nil, fmt.Errorf("brain %s is not running", brain)
	}
	calendars, err := google.CalendarsOf(br.Manifest()).Pick("", true)
	if err != nil {
		return nil, err
	}
	tok, err := b.google.AccessToken(ctx, brain)
	if err != nil {
		return nil, err
	}
	// From the next half hour after the lead, to the end of the last day.
	from := now.Add(errandBookingLead).Add(30*time.Minute - time.Nanosecond).Truncate(30 * time.Minute)
	day := now.In(loc)
	to := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, c.Ahead()+1)
	busy, err := b.google.BusyTimes(ctx, tok, calendars, from, to, googleZone(loc))
	if err != nil {
		return nil, err
	}
	var spans []errand.Span
	for _, s := range busy {
		start, err1 := time.Parse(time.RFC3339, s.Start)
		end, err2 := time.Parse(time.RFC3339, s.End)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("a busy time Google gave, %s–%s, is no time", s.Start, s.End)
		}
		spans = append(spans, errand.Span{Start: start, End: end})
	}
	hFrom, hTo := c.Hours()
	return errand.FreeTimes(spans, from, to, errand.Hours{Days: c.Weekdays(), From: hFrom, To: hTo, Location: loc}), nil
}

// errandCalendarDoc is what errand e's run is told of the calendar, nil
// when it may not use it.
func (b *App) errandCalendarDoc(ctx context.Context, e store.Errand, s manifest.Errands, now time.Time, loc *time.Location) *errand.Calendar {
	mode, c := b.errandCalendarOf(ctx, e, s)
	if mode == errand.CalendarOff {
		return nil
	}
	doc := &errand.Calendar{May: mode, MaxMinutes: c.Longest()}
	if e.Booked != "" {
		// One booking an errand: the run may still offer times, to talk
		// about another, but not book.
		doc.May = errand.CalendarOffer
		when := errand.FormatSpan(errand.Span{Start: e.BookedFrom, End: e.BookedTo}, loc)
		doc.Note = "This conversation has its booking, " + when + ": no other."
		if strings.HasPrefix(e.Booked, store.BookingAsking) {
			doc.Note = "The owner is asked to confirm " + when + ": no other booking meanwhile."
		}
	}
	free, err := b.errandFreeTimes(ctx, e.Brain, c, now, loc)
	if err != nil {
		log.Printf("errand %s: reading the calendar: %v", e.ID, err)
		doc.Note = strings.TrimSpace(doc.Note + " The calendar cannot be read just now: offer no times, and say the owner will come back to them about one.")
		return doc
	}
	for _, f := range free {
		doc.Free = append(doc.Free, errand.FormatSpan(f, loc))
	}
	if len(free) == 0 && doc.Note == "" {
		doc.Note = fmt.Sprintf("No free times in the next %d days: say the owner will come back to them about a time.", c.Ahead())
	}
	return doc
}

// errandRespond has errand e's run answer, and books what it asks to
// book. A booking the bridge does not make goes back to the run as a
// line from the calendar, which says why; after a second, the owner is
// asked instead.
func (b *App) errandRespond(ctx context.Context, e store.Errand, s manifest.Errands, lines []store.ErrandLine, call bool) (errand.Answer, error) {
	for try := 0; ; try++ {
		ans, err := b.errandAsk(ctx, e, s, lines, call)
		if err != nil || ans.Action != errand.Book {
			return ans, err
		}
		said, err := b.bookInErrand(ctx, e, s, ans)
		line := store.ErrandLine{Sender: errand.FromCalendar, Text: said, At: time.Now()}
		if err := b.store.AddErrandLine(ctx, e.ID, line, false); err != nil {
			log.Printf("errand %s: recording the booking: %v", e.ID, err)
		}
		lines = append(lines, line)
		if err == nil {
			return ans, nil
		}
		if try == 1 {
			text := outboundmsg.ErrandBookingCheckMessage(strings.TrimSpace(s.OwnerName))
			if call {
				// Read aloud without its emoji.
				text = strings.TrimLeftFunc(text, func(r rune) bool { return !unicode.IsLetter(r) })
			}
			return errand.Answer{Action: errand.AskOwner, Text: text, Question: outboundmsg.ErrandBookingQuestion(said)}, nil
		}
		if fresh, err := b.store.GetErrand(ctx, e.ID); err == nil {
			e = fresh
		}
	}
}

// bookInErrand books what errand e's run asked to (ans), or asks the
// owner to confirm it first, as the errand may; and says what came of it,
// for the run. An error is a booking it did not make, nor ask for.
func (b *App) bookInErrand(ctx context.Context, e store.Errand, s manifest.Errands, ans errand.Answer) (string, error) {
	loc := RuntimeConfig(b.cfgs).Location()
	mode, c := b.errandCalendarOf(ctx, e, s)
	notBooked := func(why string) (string, error) {
		b.auditErrand(e, "not booked", why)
		return "Not booked: " + why + ".", errNotBooked
	}
	switch {
	case !errand.MayBook(mode):
		return notBooked("this conversation may only offer free times, not book one")
	case e.Booked != "":
		return notBooked("this conversation has its booking already")
	case ans.BookMinutes < 15 || ans.BookMinutes > c.Longest():
		return notBooked(fmt.Sprintf("a booking is 15 to %d minutes long", c.Longest()))
	}
	start, err := errand.ParseBookStart(ans.BookStart, loc)
	if err != nil {
		return notBooked(err.Error())
	}
	span := errand.Span{Start: start, End: start.Add(time.Duration(ans.BookMinutes) * time.Minute)}
	when := errand.FormatSpan(span, loc)
	free, err := b.errandFreeTimes(ctx, e.Brain, c, time.Now(), loc)
	if err != nil {
		log.Printf("errand %s: reading the calendar: %v", e.ID, err)
		return notBooked("the calendar cannot be read just now")
	}
	if !errand.Within(span, free) {
		return notBooked(when + " is not one of the free times")
	}
	owner := strings.TrimSpace(s.OwnerName)
	if mode == errand.CalendarAsk {
		if err := b.askToBook(ctx, e, span, when); err != nil {
			log.Printf("errand %s: asking to book: %v", e.ID, err)
			return notBooked("the owner cannot be asked just now")
		}
		return fmt.Sprintf("Asked %s to confirm %s. You will hear their answer here.", owner, when), nil
	}
	if ok, err := b.store.SetErrandBooking(ctx, e.ID, "", store.BookingMaking, span.Start, span.End); err != nil || !ok {
		return notBooked("this conversation has its booking already")
	}
	ev, err := b.insertErrandEvent(ctx, e, span, loc)
	if err != nil {
		log.Printf("errand %s: booking %s: %v", e.ID, when, err)
		_, _ = b.store.SetErrandBooking(ctx, e.ID, store.BookingMaking, "", time.Time{}, time.Time{})
		return notBooked("the calendar could not take it just now")
	}
	_, _ = b.store.SetErrandBooking(ctx, e.ID, store.BookingMaking, store.BookingEvent+ev.ID, span.Start, span.End)
	b.auditErrand(e, "booked", when)
	b.tellErrandOwner(ctx, e, outboundmsg.ErrandBookedNotice(e.Name, e.ID, when))
	return fmt.Sprintf("Booked %s in %s's calendar.", when, owner), nil
}

// askToBook asks the owner, in a poll, to confirm booking span (when)
// for errand e.
func (b *App) askToBook(ctx context.Context, e store.Errand, span errand.Span, when string) error {
	now := time.Now()
	id := "a" + randomToken(9)
	if ok, err := b.store.SetErrandBooking(ctx, e.ID, "", store.BookingAsking+id, span.Start, span.End); err != nil || !ok {
		return errors.Join(errors.New("it has its booking already"), err)
	}
	details, _ := json.Marshal(errandBookingRequest{Errand: e.ID, From: span.Start, To: span.End})
	a := store.Approval{Kind: approvalKind, ID: id, Brain: e.Brain, Run: "errand:" + e.ID, Thread: e.Chat, Chat: e.Chat, Notify: errandBookingNotify, Risk: "write",
		Action: fmt.Sprintf("Book %s with %s", when, e.Name), Details: string(details), CreatedAt: now, ExpiresAt: now.Add(b.approvalTimeout())}
	maxChunk := b.cfgs.Get().MaxChunkSize
	err := b.store.AddApproval(ctx, a)
	if err == nil {
		err = b.outbox.Text(ctx, e.Chat, outboundmsg.ErrandBookingDraft(e.Name, e.ID, when), maxChunk)
	}
	if err == nil {
		err = b.outbox.Poll(ctx, e.Chat, outboundmsg.ApprovalQuestion(a.Action), []string{outboundmsg.ApprovalYes, outboundmsg.ApprovalNo}, approvalRefPrefix+a.ID)
	}
	if err != nil {
		_, _ = b.store.DecideApproval(ctx, a.ID, store.ApprovalDenied, "", "bridge", now)
		_, _ = b.store.SetErrandBooking(ctx, e.ID, store.BookingAsking+id, "", time.Time{}, time.Time{})
		return err
	}
	b.audit.Append(audit.Entry{Kind: audit.ApprovalRequested, Chat: e.Chat, Brain: a.Brain, Run: a.Run, ID: a.ID, Risk: a.Risk, Detail: a.Action, Outcome: "pending"})
	b.auditErrand(e, "asked to book", when)
	return nil
}

// errandBookingDecided takes the owner's answer to a booking's poll: on
// a yes it books the time, if it is still free; either way the errand's
// run hears of it, as a line from the calendar, and tells the person.
func (b *App) errandBookingDecided(a store.Approval) {
	ctx := context.Background()
	var req errandBookingRequest
	if err := json.Unmarshal([]byte(a.Details), &req); err != nil {
		log.Printf("errand: approval %s is no booking: %v", a.ID, err)
		return
	}
	e, err := b.store.GetErrand(ctx, req.Errand)
	if err != nil || e.Booked != store.BookingAsking+a.ID {
		log.Printf("errand %s: the answer to booking %s came, which it no longer waits for", req.Errand, a.ID)
		return
	}
	s, _ := b.errandSettings(e.Brain)
	owner := strings.TrimSpace(s.OwnerName)
	loc := RuntimeConfig(b.cfgs).Location()
	span := errand.Span{Start: req.From, End: req.To}
	when := errand.FormatSpan(span, loc)
	tell := func(text string) {
		if err := b.store.AddErrandLine(ctx, e.ID, store.ErrandLine{Sender: errand.FromCalendar, Text: text, At: time.Now()}, false); err != nil {
			log.Printf("errand %s: recording the booking: %v", e.ID, err)
			return
		}
		if e.State == store.ErrandOpen {
			b.errands.soon(e.ID, 0, b.errandTurn)
		}
	}
	clear := func() {
		_, _ = b.store.SetErrandBooking(ctx, e.ID, store.BookingAsking+a.ID, "", time.Time{}, time.Time{})
	}
	if e.State != store.ErrandOpen {
		// Nobody would tell the person of it any more (#639): the owner
		// hears instead, whatever they answered.
		clear()
		b.auditErrand(e, "not booked", "the errand had ended")
		b.tellErrandOwner(ctx, e, outboundmsg.ErrandBookingEndedNotice(e.Name, e.ID, when))
		return
	}
	if a.Status != store.ApprovalApproved {
		clear()
		why := "they said no"
		if a.Status != store.ApprovalDenied {
			why = "they did not answer in time"
		}
		b.auditErrand(e, "not booked", why)
		tell(fmt.Sprintf("%s did not confirm %s (%s): it is not booked. Tell them, and offer other free times.", owner, when, why))
		return
	}
	mode, c := b.errandCalendarOf(ctx, e, s)
	free, err := b.errandFreeTimes(ctx, e.Brain, c, time.Now(), loc)
	switch {
	case !errand.MayBook(mode):
		clear()
		b.auditErrand(e, "not booked", "the calendar is off for it now")
		tell(fmt.Sprintf("%s confirmed %s, but this conversation may no longer book: it is not booked. Say %s will come back to them about it.", owner, when, owner))
		return
	case err != nil:
		log.Printf("errand %s: reading the calendar: %v", e.ID, err)
		clear()
		b.auditErrand(e, "not booked", "the calendar cannot be read just now")
		tell(fmt.Sprintf("%s confirmed %s, but the calendar cannot be read just now: it is not booked. Say %s will come back to them about it.", owner, when, owner))
		return
	case !errand.Within(span, free):
		clear()
		b.auditErrand(e, "not booked", when+" is no longer free")
		tell(fmt.Sprintf("%s is no longer free: it is not booked. Tell them, and offer other free times.", when))
		return
	}
	if ok, _ := b.store.SetErrandBooking(ctx, e.ID, store.BookingAsking+a.ID, store.BookingMaking, span.Start, span.End); !ok {
		return
	}
	ev, err := b.insertErrandEvent(ctx, e, span, loc)
	if err != nil {
		log.Printf("errand %s: booking %s: %v", e.ID, when, err)
		_, _ = b.store.SetErrandBooking(ctx, e.ID, store.BookingMaking, "", time.Time{}, time.Time{})
		b.tellErrandOwner(ctx, e, outboundmsg.ErrandBookingFailedNotice(e.Name, e.ID, when))
		tell(fmt.Sprintf("%s confirmed %s, but the calendar could not take it: it is not booked. Say %s will come back to them about it.", owner, when, owner))
		return
	}
	_, _ = b.store.SetErrandBooking(ctx, e.ID, store.BookingMaking, store.BookingEvent+ev.ID, span.Start, span.End)
	b.auditErrand(e, "booked", when)
	b.tellErrandOwner(ctx, e, outboundmsg.ErrandBookedNotice(e.Name, e.ID, when))
	tell(fmt.Sprintf("%s confirmed: %s is booked in their calendar. Tell them.", owner, when))
}

// denyErrandBooking denies the booking errand id still waits for the
// owner's yes on, once it ended: errandBookingDecided then clears it and
// tells the owner.
func (b *App) denyErrandBooking(ctx context.Context, id string) {
	// Read again: the turn that ended it may have asked to book.
	e, err := b.store.GetErrand(ctx, id)
	if a, ok := strings.CutPrefix(e.Booked, store.BookingAsking); err == nil && ok {
		b.decide(ctx, a, store.ApprovalDenied, "", "bridge", answerAsMessage)
	}
}

// insertErrandEvent books span in the default calendar of errand e's
// brain: an event that names the person and the errand, and holds
// nothing they wrote.
func (b *App) insertErrandEvent(ctx context.Context, e store.Errand, span errand.Span, loc *time.Location) (google.CalendarEvent, error) {
	br := b.brainMgr.Get(e.Brain)
	if br == nil {
		return google.CalendarEvent{}, fmt.Errorf("brain %s is not running", e.Brain)
	}
	target, err := google.CalendarsOf(br.Manifest()).Pick("", false)
	if err != nil {
		return google.CalendarEvent{}, err
	}
	tok, err := b.google.AccessToken(ctx, e.Brain)
	if err != nil {
		return google.CalendarEvent{}, err
	}
	ev := google.NewEvent{
		Title:       fmt.Sprintf("%s (arranged by Fridolin)", e.Name),
		Start:       span.Start.In(loc).Format(time.RFC3339),
		End:         span.End.In(loc).Format(time.RFC3339),
		TimeZone:    googleZone(loc),
		Description: fmt.Sprintf("Arranged by Fridolin with %s (+%s), errand %s. The conversation is on the dashboard's Errands page.", e.Name, e.Contact, e.ID),
	}
	body, err := ev.Body()
	if err != nil {
		return google.CalendarEvent{}, err
	}
	return b.google.InsertEvent(ctx, tok, target[0], body, "none")
}
