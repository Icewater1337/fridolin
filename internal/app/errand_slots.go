package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/errand"
	"github.com/Icewater1337/fridolin/internal/manifest"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
)

// Scheduling by poll (docs/BRAINS.md, "Scheduling by poll"): an errand's
// run offers up to errand.MaxSlots times in a WhatsApp poll; the bridge
// keeps only those that are free and inside the errand's window, and the
// person's vote is their choice, booked as the errand's calendar mode
// allows, after checking once more that the time is still free.

// errandSlotsRef is the ref of an errand's poll, "errand-slots:<id>".
const errandSlotsRef = "errand-slots:"

// errandWindow is the window errand e was opened with, nil for none.
func errandWindow(e store.Errand) *errand.Window {
	var w errand.Window
	if e.Window == "" || json.Unmarshal([]byte(e.Window), &w) != nil {
		return nil
	}
	return &w
}

// pollSlots keeps of ans's slots those that are free and inside e's
// window, and says why when none is.
func (b *App) pollSlots(ctx context.Context, e store.Errand, s manifest.Errands, ans errand.Answer) ([]errand.Span, error) {
	loc := RuntimeConfig(b.cfgs).Location()
	mode, c := b.errandCalendarOf(ctx, e, s)
	switch {
	case mode == errand.CalendarOff:
		return nil, errors.New("this conversation may not use the calendar")
	case e.Booked != "":
		return nil, errors.New("this conversation has its booking already")
	case ans.BookMinutes < 15 || ans.BookMinutes > c.Longest():
		// A vote books through bookInErrand, which takes no other length.
		return nil, fmt.Errorf("a booking is 15 to %d minutes long", c.Longest())
	}
	free, err := b.errandFreeTimes(ctx, e.Brain, c, time.Now(), loc)
	if err != nil {
		log.Printf("errand %s: reading the calendar: %v", e.ID, err)
		return nil, errors.New("the calendar cannot be read just now")
	}
	w := errandWindow(e)
	var out []errand.Span
	for _, str := range ans.Slots {
		start, err := errand.ParseBookStart(str, loc)
		if err != nil {
			continue
		}
		span := errand.Span{Start: start, End: start.Add(time.Duration(ans.BookMinutes) * time.Minute)}
		if errand.Within(span, free) && w.Allows(span, loc) && !slices.Contains(out, span) {
			out = append(out, span)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("none of those times is free and inside what the owner allows; offer times from calendar.free_times within calendar.window")
	}
	return out, nil
}

// sendSlotPoll sends errand e's person a poll of spans and keeps them for
// the vote.
func (b *App) sendSlotPoll(ctx context.Context, e store.Errand, spans []errand.Span) error {
	loc := RuntimeConfig(b.cfgs).Location()
	options := make([]string, 0, len(spans)+1)
	raw := make([][2]time.Time, 0, len(spans))
	for _, sp := range spans {
		options = append(options, errand.FormatSpan(sp, loc))
		raw = append(raw, [2]time.Time{sp.Start, sp.End})
	}
	options = append(options, outboundmsg.ErrandSlotsNone)
	j, _ := json.Marshal(raw)
	if err := b.store.SetErrandSlots(ctx, e.ID, string(j)); err != nil {
		return err
	}
	if err := b.outbox.Poll(ctx, chat.DMJID(e.Contact), outboundmsg.ErrandSlotsQuestion, options, errandSlotsRef+e.ID); err != nil {
		return err
	}
	if err := b.store.AddErrandLine(ctx, e.ID, store.ErrandLine{Sender: errand.FromYou, Text: "[poll] " + strings.Join(options, " | "), At: time.Now()}, true); err != nil {
		log.Printf("errand %s: recording the poll: %v", e.ID, err)
	}
	b.auditErrand(e, "polled", fmt.Sprintf("%d times", len(spans)))
	return nil
}

// errandSlotsVote takes the person's vote in errand id's poll: a time is
// booked (bookInErrand honours the mode and checks it is still free), or
// in offer mode passed to the run and the owner as their choice; none
// goes back to the run. Either way the run hears of it and tells them.
// The poll's times stay until a time is taken, so that a vote changed
// from none to a time still counts.
func (b *App) errandSlotsVote(id string, v pollVote) {
	ctx := context.Background()
	e, err := b.store.GetErrand(ctx, id)
	if err != nil || e.State != store.ErrandOpen || len(v.Selected) == 0 || chat.IdentityKey(v.Voter.User) != chat.IdentityKey(e.Contact) {
		return
	}
	var raw [][2]time.Time
	if json.Unmarshal([]byte(e.Slots), &raw) != nil || len(raw) == 0 {
		return // an older poll, or one already answered
	}
	s, _ := b.errandSettings(e.Brain)
	loc := RuntimeConfig(b.cfgs).Location()
	tell := func(text string) {
		if err := b.store.AddErrandLine(ctx, e.ID, store.ErrandLine{Sender: errand.FromCalendar, Text: text, At: time.Now()}, false); err != nil {
			log.Printf("errand %s: recording the vote: %v", e.ID, err)
			return
		}
		b.errands.soon(e.ID, 0, b.errandTurn)
	}
	if v.Selected[0] == outboundmsg.ErrandSlotsNone {
		b.auditErrand(e, "voted", "none of the times")
		tell("They voted that none of the offered times work. Ask what suits them, or poll other times.")
		return
	}
	for _, r := range raw {
		span := errand.Span{Start: r[0], End: r[1]}
		if errand.FormatSpan(span, loc) != v.Selected[0] {
			continue
		}
		_ = b.store.SetErrandSlots(ctx, e.ID, "")
		b.auditErrand(e, "voted", v.Selected[0])
		if mode, _ := b.errandCalendarOf(ctx, e, s); !errand.MayBook(mode) {
			b.tellErrandOwner(ctx, e, outboundmsg.ErrandChosenNotice(e.Name, e.ID, v.Selected[0]))
			tell(fmt.Sprintf("They chose %s; nothing is booked. Say %s will confirm.", v.Selected[0], strings.TrimSpace(s.OwnerName)))
			return
		}
		said, err := b.bookInErrand(ctx, e, s, errand.Answer{BookStart: span.Start.In(loc).Format("2006-01-02 15:04"), BookMinutes: int(span.End.Sub(span.Start) / time.Minute)})
		if err != nil {
			said += " Tell them, and offer the next free times."
		} else {
			said += " Their vote was their choice: tell them."
		}
		tell(said)
		return
	}
}

// answerSpans are the times of a poll answer, whose slots errandRespond
// already checked.
func answerSpans(ans errand.Answer, loc *time.Location) []errand.Span {
	var out []errand.Span
	for _, str := range ans.Slots {
		if t, err := errand.ParseBookStart(str, loc); err == nil {
			out = append(out, errand.Span{Start: t, End: t.Add(time.Duration(ans.BookMinutes) * time.Minute)})
		}
	}
	return out
}
