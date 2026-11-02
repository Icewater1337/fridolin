package app

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/errand"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/store"
)

// The Errands page (docs/BRAINS.md, "Errands"): every brain's errands
// with their conversations, and the people Fridolin stopped listening to.
// Its Stop and Unblock do what errand_stop and !unblock do.

// errandsEndedShown is how many ended errands the page lists.
const errandsEndedShown = 50

func (b *App) newErrandsAPI() *httpapi.ErrandsAPI {
	return &httpapi.ErrandsAPI{List: b.errandsList, Get: b.errandDetail, Stop: b.dashboardStopErrand, Unblock: b.dashboardUnblock}
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
	out := httpapi.ErrandsResponse{Errands: []httpapi.ErrandView{}, Blocked: []httpapi.BlockedContact{}}
	list, err := b.store.AllErrands(ctx, errandsEndedShown)
	if err != nil {
		return out, err
	}
	for _, e := range list {
		out.Errands = append(out.Errands, errandViewOf(e))
	}
	blocked, err := b.store.BlockedErrandContacts(ctx)
	if err != nil {
		return out, err
	}
	for _, c := range blocked {
		out.Blocked = append(out.Blocked, httpapi.BlockedContact{Number: c.Contact, Masked: maskedNumber(c.Contact), Name: c.Name, Brain: c.Brain, Reason: c.Reason, Since: c.UpdatedAt})
	}
	return out, nil
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
	out := httpapi.ErrandDetail{ErrandView: errandViewOf(e), Goal: e.Goal, Brief: e.Brief, ByOwner: e.ByOwner, Days: e.Days, MaxMessages: errand.MaxMessages, Lines: []httpapi.ErrandLine{}}
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
