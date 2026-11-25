package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Icewater1337/fridolin/internal/google"
)

// MailWriteGuard is CalendarWriteGuard for a brain's Gmail writes, which also
// leaves an audit line of its own.
type MailWriteGuard interface {
	CalendarWriteGuard
	RecordMail(tok string, w MailWrite)
}

// MailWrite is one Gmail write as its audit line has it: never the
// subject, the body or an address.
type MailWrite struct {
	// Action is send, label, archive, trash or delete.
	Action string
	// Message is the message's id, "" for a send that did not happen.
	Message string
	// Recipients is how many people a send reaches.
	Recipients int
	Approval   string
	// Outcome is ok, or why it was refused or failed.
	Outcome string
}

type mailWrite struct {
	a   *BrainAPI
	tok string
	w   MailWrite
}

func (a *BrainAPI) mailWrite(r *http.Request, action, message string) *mailWrite {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return &mailWrite{a: a, tok: tok, w: MailWrite{Action: action, Message: message}}
}

func (m *mailWrite) refused(msg string) error {
	m.w.Outcome = "refused: " + msg
	m.a.MailWrites.RecordMail(m.tok, m.w)
	return &callError{status: http.StatusForbidden, msg: msg}
}

func (m *mailWrite) allow() error {
	if err := m.a.MailWrites.Allow(m.tok); err != nil {
		return m.refused(err.Error())
	}
	return nil
}

// approve takes the owner's yes (an approved request_approval's id) for a
// step that cannot be undone.
func (m *mailWrite) approve(ctx context.Context, id, ask string) error {
	if id == "" {
		return m.refused(ask)
	}
	if err := m.a.MailWrites.UseApproval(ctx, m.tok, id); err != nil {
		return m.refused(err.Error() + ". Do not take the step.")
	}
	m.w.Approval = id
	return nil
}

func (m *mailWrite) done(err error) error {
	m.w.Outcome = "ok"
	if err != nil {
		_, msg := google.CallProblem(err)
		m.w.Outcome = "failed: " + msg
	}
	m.a.MailWrites.RecordMail(m.tok, m.w)
	return err
}

// mailToken is token(), and checks the grant holds Gmail before anything
// is asked of the owner.
func mailToken(token func() (*google.Token, error)) (*google.Token, error) {
	tok, err := token()
	if err == nil && !google.HasGmail(tok.Scopes) {
		err = google.ErrNoGmail
	}
	return tok, err
}

// brainMailList answers GET /brain/gmail/messages?q=&max=: a Gmail search,
// newest first (default 20 messages, at most 100).
func (a *BrainAPI) brainMailList(ctx context.Context, r *http.Request, _ google.Calendars, token func() (*google.Token, error)) (any, error) {
	limit := 20
	if s := r.URL.Query().Get("max"); s != "" {
		var err error
		if limit, err = strconv.Atoi(s); err != nil || limit < 1 || limit > 100 {
			return nil, badCall("max must be a number from 1 to 100")
		}
	}
	tok, err := mailToken(token)
	if err != nil {
		return nil, err
	}
	return a.Google().ListMail(ctx, tok, r.URL.Query().Get("q"), limit)
}

// brainMailGet answers GET /brain/gmail/messages/{id}.
func (a *BrainAPI) brainMailGet(ctx context.Context, r *http.Request, _ google.Calendars, token func() (*google.Token, error)) (any, error) {
	tok, err := mailToken(token)
	if err != nil {
		return nil, err
	}
	return a.Google().GetMail(ctx, tok, r.PathValue("id"))
}

// brainMailSend answers POST /brain/gmail/send with a MailDraft. A send
// cannot be taken back and reaches other people, so it always needs the
// owner's yes: request_approval, then the approval_id here.
func (a *BrainAPI) brainMailSend(ctx context.Context, r *http.Request, _ google.Calendars, token func() (*google.Token, error)) (any, error) {
	var d google.MailDraft
	if err := decodeSingleJSON(r.Body, &d); err != nil {
		return nil, badCall("the message is not valid JSON: %v", google.DecodeError(err))
	}
	w := a.mailWrite(r, "send", d.ReplyTo)
	w.w.Recipients = d.Recipients()
	if err := w.allow(); err != nil {
		return nil, err
	}
	tok, err := mailToken(token)
	if err != nil {
		return nil, w.done(err)
	}
	ask := "sending an email cannot be taken back and reaches other people: that needs the owner's yes first. " +
		"Call request_approval with the recipients, the subject and what the message says, and once it answers approved, " +
		"send it again with its approval_id (fridolin gmail send … --approval-id ID)."
	if err := w.approve(ctx, d.ApprovalID, ask); err != nil {
		return nil, err
	}
	sent, err := a.Google().SendMail(ctx, tok, d)
	if sent != nil {
		w.w.Message = sent.ID
	}
	if err := w.done(err); err != nil {
		return nil, err
	}
	return sent, nil
}

// MailLabels is the body of POST /brain/gmail/messages/{id}/labels.
type MailLabels struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

// MailChanged is the answer of the calls that change one message.
type MailChanged struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

// brainMailLabels answers POST /brain/gmail/messages/{id}/labels, and with
// archive set removes INBOX instead.
func (a *BrainAPI) brainMailLabels(archive bool) calendarHandler {
	return func(ctx context.Context, r *http.Request, _ google.Calendars, token func() (*google.Token, error)) (any, error) {
		id := r.PathValue("id")
		var l MailLabels
		action := "archive"
		if archive {
			l.Remove = []string{"INBOX"}
		} else {
			action = "label"
			if err := decodeSingleJSON(r.Body, &l); err != nil {
				return nil, badCall("the request is not valid JSON: %v", google.DecodeError(err))
			}
			if len(l.Add)+len(l.Remove) == 0 {
				return nil, badCall("name a label to add or remove")
			}
		}
		w := a.mailWrite(r, action, id)
		if err := w.allow(); err != nil {
			return nil, err
		}
		tok, err := mailToken(token)
		if err != nil {
			return nil, w.done(err)
		}
		if err := w.done(a.Google().ModifyMailLabels(ctx, tok, id, l.Add, l.Remove)); err != nil {
			return nil, err
		}
		return MailChanged{ID: id, Action: action}, nil
	}
}

// brainMailTrash answers POST /brain/gmail/messages/{id}/trash: Gmail keeps
// a trashed message 30 days, so it needs no approval.
func (a *BrainAPI) brainMailTrash(ctx context.Context, r *http.Request, _ google.Calendars, token func() (*google.Token, error)) (any, error) {
	id := r.PathValue("id")
	w := a.mailWrite(r, "trash", id)
	if err := w.allow(); err != nil {
		return nil, err
	}
	tok, err := mailToken(token)
	if err != nil {
		return nil, w.done(err)
	}
	if err := w.done(a.Google().TrashMail(ctx, tok, id)); err != nil {
		return nil, err
	}
	return MailChanged{ID: id, Action: "trash"}, nil
}

// brainMailDelete answers DELETE /brain/gmail/messages/{id}?approval_id=:
// a permanent delete, which needs the owner's yes.
func (a *BrainAPI) brainMailDelete(ctx context.Context, r *http.Request, _ google.Calendars, token func() (*google.Token, error)) (any, error) {
	id := r.PathValue("id")
	w := a.mailWrite(r, "delete", id)
	if err := w.allow(); err != nil {
		return nil, err
	}
	tok, err := mailToken(token)
	if err != nil {
		return nil, w.done(err)
	}
	ask := fmt.Sprintf("deleting message %s for good cannot be undone (trash is the way back): that needs the owner's yes first. "+
		"Call request_approval with which message you will delete and why, and once it answers approved, "+
		"delete it again with its approval_id (fridolin gmail delete --id … --approval-id ID).", id)
	if err := w.approve(ctx, r.URL.Query().Get("approval_id"), ask); err != nil {
		return nil, err
	}
	if err := w.done(a.Google().DeleteMail(ctx, tok, id)); err != nil {
		return nil, err
	}
	return MailChanged{ID: id, Action: "delete"}, nil
}
