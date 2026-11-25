package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/skill"
)

// calendarWrites is httpapi.CalendarWriteGuard: a brain's calendar writes
// (fridolin calendar create and delete) go through M5's policy as the
// bridge's tools that write do (mcpGuard): no lockdown, a run whose risk
// level allows writing, and for a write that reaches other people an
// approval of the owner's; each one is an audit line.
type calendarWrites struct{ b *App }

// caller is the run tok stands for, as the policy's checks take it.
func (w calendarWrites) caller(tok string) mcpCaller {
	info, _ := w.b.runTokens.runOf(tok)
	return mcpCaller{run: info}
}

func (w calendarWrites) Allow(tok string) error {
	if w.b.locked() {
		return errors.New("the bridge is in lockdown: only reading goes ahead, so no calendar event is created or deleted. Do not try another way; say so in your reply")
	}
	if risk := w.caller(tok).risk(); risk.Rank() < skill.RiskWrite.Rank() {
		return fmt.Errorf("this run's risk level is %s, which does not allow writing to the calendar", risk)
	}
	return nil
}

func (w calendarWrites) UseApproval(ctx context.Context, tok, id string) error {
	return w.b.useApproval(ctx, w.caller(tok), id)
}

func (w calendarWrites) Record(tok string, cw httpapi.CalendarWrite) {
	detail := cw.Action + " in " + cw.Calendar
	if cw.Guests > 0 {
		detail += fmt.Sprintf(", %d attendee", cw.Guests)
		if cw.Guests > 1 {
			detail += "s"
		}
	}
	if cw.SendUpdates != "" {
		detail += ", send_updates " + cw.SendUpdates
	}
	outcome := cw.Outcome
	if cw.Approval != "" {
		outcome += " (approval " + cw.Approval + ")"
	}
	e := runEntry(w.caller(tok).run, audit.CalendarWrite)
	e.ID, e.Detail, e.Outcome = cw.Event, detail, outcome
	w.b.audit.Append(e)
}

// runEntry is an audit line of kind about run: its brain, run, chat and
// thread, and the skill and risk of its policy.
func runEntry(run runInfo, kind string) audit.Entry {
	e := audit.Entry{Kind: kind, Brain: run.Brain, Run: run.ID, Chat: run.Chat, Thread: run.Chat}
	if p := run.Policy; p != nil {
		e.Thread, e.Skill, e.Risk = p.Thread, p.Skill, string(p.Risk)
	}
	return e
}

// RecordMail is httpapi.MailWriteGuard's audit line.
func (w calendarWrites) RecordMail(tok string, mw httpapi.MailWrite) {
	detail := mw.Action
	if mw.Recipients > 0 {
		detail += fmt.Sprintf(" to %d", mw.Recipients)
	}
	outcome := mw.Outcome
	if mw.Approval != "" {
		outcome += " (approval " + mw.Approval + ")"
	}
	e := runEntry(w.caller(tok).run, audit.MailWrite)
	e.ID, e.Detail, e.Outcome = mw.Message, detail, outcome
	w.b.audit.Append(e)
}
