package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/errand"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/store"
)

// The Errands page (docs/BRAINS.md, "Errands"): every brain's errands
// with their conversations, and the people Fridolin knows. Its Stop,
// Unblock and the people's settings do what errand_stop, !unblock and
// errand_contact do.

// errandsEndedShown is how many ended errands the page lists.
const errandsEndedShown = 50

func (b *App) newErrandsAPI() *httpapi.ErrandsAPI {
	return &httpapi.ErrandsAPI{List: b.errandsList, Get: b.errandDetail, Stop: b.dashboardStopErrand, Unblock: b.dashboardUnblock, SetPerson: b.dashboardSetPerson}
}

// maskedNumber is a person's number as the audit log writes it.
func maskedNumber(n string) string { return "+" + logx.MaskPhone(n) }

func errandViewOf(e store.Errand) httpapi.ErrandView {
	v := httpapi.ErrandView{ID: e.ID, Brain: e.Brain, Name: e.Name, Number: e.Contact, Masked: maskedNumber(e.Contact), State: e.State, Live: e.Live(),
		Sent: e.Sent, Created: e.CreatedAt, Deadline: e.Deadline, Outcome: e.Outcome}
	if !e.ClosedAt.IsZero() {
		closed := e.ClosedAt
		v.Closed = &closed
	}
	return v
}

// errandsList answers GET /api/errands.
func (b *App) errandsList(ctx context.Context) (httpapi.ErrandsResponse, error) {
	out := httpapi.ErrandsResponse{Errands: []httpapi.ErrandView{}, People: []httpapi.ErrandPerson{}}
	list, err := b.store.AllErrands(ctx, errandsEndedShown)
	if err != nil {
		return out, err
	}
	for _, e := range list {
		out.Errands = append(out.Errands, errandViewOf(e))
	}
	people, err := b.store.ErrandContacts(ctx)
	if err != nil {
		return out, err
	}
	for _, c := range people {
		out.People = append(out.People, errandPersonOf(c))
	}
	return out, nil
}

func errandPersonOf(c store.ErrandContact) httpapi.ErrandPerson {
	p := httpapi.ErrandPerson{Number: c.Contact, Masked: maskedNumber(c.Contact), Name: c.Name, Brain: c.Brain, State: c.State, Reason: c.Reason,
		Mode: c.Mode, Brief: c.Brief, Calendar: c.Calendar, Since: c.UpdatedAt}
	if c.Warned(time.Now()) {
		warned := c.WarnedAt
		p.Warned = &warned
	}
	return p
}

// checkContactWays reports what is wrong with setting contact's mode,
// brief and calendar, "" for nothing: what errand_contact and the Errands
// page refuse alike.
func checkContactWays(contact store.ErrandContact, mode, brief, calendar string) (string, apperr.Kind) {
	switch {
	case calendar != "" && !slices.Contains(errand.CalendarModes, calendar):
		return "calendar is off, offer, book_after_yes, book, or \"\" for what brain.json allows", apperr.Invalid
	case contact.State == store.ContactBlocked:
		return fmt.Sprintf("+%s is blocked: only the owner lifts that, with !unblock %s", contact.Contact, contact.Contact), apperr.Conflict
	case mode != store.ContactConverse && mode != store.ContactForward:
		return "mode is converse or forward", apperr.Invalid
	case len([]rune(brief)) > errand.MaxForThem:
		return fmt.Sprintf("brief is at most %d characters", errand.MaxForThem), apperr.Invalid
	}
	return "", apperr.Unknown
}

// dashboardSetPerson answers PUT /api/errand-contacts/{number}: how
// Fridolin deals with someone it knows, as errand_contact sets it.
func (b *App) dashboardSetPerson(ctx context.Context, written string, ways httpapi.PersonWays) (httpapi.ErrandPerson, error) {
	number, err := chat.PhoneNumber(written)
	if err != nil {
		return httpapi.ErrandPerson{}, apperr.Wrap(apperr.Invalid, err)
	}
	contact, err := b.store.ErrandContactOf(ctx, number)
	if errors.Is(err, store.ErrNoErrand) {
		return httpapi.ErrandPerson{}, apperr.New(apperr.NotFound, "Fridolin never wrote to +"+number)
	}
	if err != nil {
		return httpapi.ErrandPerson{}, err
	}
	mode, brief, calendar := strings.TrimSpace(ways.Mode), strings.TrimSpace(ways.Brief), strings.TrimSpace(ways.Calendar)
	if why, kind := checkContactWays(contact, mode, brief, calendar); why != "" {
		return httpapi.ErrandPerson{}, apperr.New(kind, why)
	}
	if _, err := b.store.SetErrandContactWays(ctx, number, mode, brief, calendar, time.Now()); err != nil {
		return httpapi.ErrandPerson{}, err
	}
	b.audit.Append(audit.Entry{Kind: audit.Errand, By: "dashboard", ID: logx.MaskJID(number, "", ""), Detail: "contact", Outcome: mode})
	if contact, err = b.store.ErrandContactOf(ctx, number); err != nil {
		return httpapi.ErrandPerson{}, err
	}
	return errandPersonOf(contact), nil
}

// errandDetail answers GET /api/errands/{id}.
func (b *App) errandDetail(ctx context.Context, id string) (httpapi.ErrandDetail, error) {
	e, err := b.store.GetErrand(ctx, id)
	if errors.Is(err, store.ErrNoErrand) {
		return httpapi.ErrandDetail{}, apperr.New(apperr.NotFound, "no errand "+id)
	}
	if err != nil {
		return httpapi.ErrandDetail{}, err
	}
	lines, err := b.store.ErrandLines(ctx, e.ID)
	if err != nil {
		return httpapi.ErrandDetail{}, err
	}
	out := httpapi.ErrandDetail{ErrandView: errandViewOf(e), Goal: e.Goal, Brief: e.Brief, ByOwner: e.ByOwner, Days: e.Days, MaxMessages: errand.MaxMessages, Calendar: e.Calendar, Lines: []httpapi.ErrandLine{}}
	if e.Booked != "" {
		from, to := e.BookedFrom, e.BookedTo
		out.BookingFrom, out.BookingTo = &from, &to
		switch {
		case strings.HasPrefix(e.Booked, store.BookingAsking):
			out.Booking = "asking"
		case strings.HasPrefix(e.Booked, store.BookingEvent):
			out.Booking = "booked"
		default:
			out.Booking = "booking"
		}
	}
	for _, l := range lines {
		out.Lines = append(out.Lines, httpapi.ErrandLine{From: l.Sender, Text: l.Text, At: l.At})
	}
	return out, nil
}

// dashboardStopErrand answers POST /api/errands/{id}/stop: the errand
// ends without a word to the person, as with the brain's errand_stop.
func (b *App) dashboardStopErrand(ctx context.Context, id string) error {
	e, err := b.store.GetErrand(ctx, id)
	if errors.Is(err, store.ErrNoErrand) {
		return apperr.New(apperr.NotFound, "no errand "+id)
	}
	if err != nil {
		return err
	}
	if !e.Live() {
		return apperr.New(apperr.Conflict, fmt.Sprintf("errand %s ended already (%s)", id, e.State))
	}
	if err := b.stopErrandFor(ctx, e.Brain, e.ID, "the owner ended it in the dashboard"); err != nil {
		// It ended between the read and the stop, say.
		return apperr.Wrap(apperr.Conflict, err)
	}
	return nil
}

// dashboardUnblock answers POST /api/errand-contacts/{number}/unblock:
// Fridolin hears the person again, and asks the owner before it writes to
// them, as after !unblock, which the audit log records the same way.
func (b *App) dashboardUnblock(ctx context.Context, written string) error {
	number, err := chat.PhoneNumber(written)
	if err != nil {
		return apperr.Wrap(apperr.Invalid, err)
	}
	masked := logx.MaskJID(number, "", "")
	record := func(outcome string) {
		b.audit.Append(audit.Entry{Kind: audit.Errand, By: "dashboard", ID: masked, Detail: "unblock", Outcome: outcome})
		log.Printf("dashboard: unblock %s: %s", masked, outcome)
	}
	unblocked, err := b.unblockErrandContact(ctx, number)
	switch {
	case err != nil:
		record("failed: " + err.Error())
		return err
	case !unblocked:
		record("nothing blocked")
		return apperr.New(apperr.NotFound, "+"+number+" is not blocked")
	}
	record("unblocked")
	return nil
}
