package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/audit"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/google"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

type BrainCalendarService interface {
	AccessToken(context.Context, string) (*google.Token, error)
	CalendarList(context.Context, *google.Token) ([]google.CalendarEntry, error)
	EventsOf(context.Context, *google.Token, []string, time.Time, time.Time, string, int) (*google.CalendarListing, error)
	InsertEvent(context.Context, *google.Token, string, map[string]any, string) (google.CalendarEvent, error)
	EventGuests(context.Context, *google.Token, string, string) (int, error)
	DeleteEvent(context.Context, *google.Token, string, string) error
	BusyTimes(context.Context, *google.Token, []string, time.Time, time.Time, string) ([]google.BusySlot, error)
	Note(string, error)
}
type BrainAPI struct {
	BrainForToken func(string) *brainruntime.Brain
	OwnsChat      func(*brainruntime.Brain, string) bool
	ReadPoll      func(context.Context, string) (string, any, error)
	RecentPolls   func(context.Context, string, int, func(string) bool) (any, int, error)
	Google        func() BrainCalendarService
	// CalendarWrites holds the calendar's writes (create, delete) to the
	// policy the bridge's tools that write are held to.
	CalendarWrites CalendarWriteGuard
	Memory         BrainMemoryService
	// MCPTools and MCPCall serve a run's MCP server (fridolin mcp --run):
	// the tools the run's token may use, and a call of one. run is the
	// run id the server was started with, which must be the token's.
	MCPTools func(ctx context.Context, token, run string) (any, error)
	MCPCall  func(ctx context.Context, token, run string, call MCPCallRequest) (any, error)
	// LiveNext and LiveEnd serve a brain's live session (fridolin mcp
	// --live, and its Stop hook, fridolin live-turn-end): the next
	// message for it, waiting up to the given time, and the end of its
	// turn with its last text and the last turn its model read.
	LiveNext func(ctx context.Context, token string, wait time.Duration) (claudecli.LiveEvent, bool, error)
	LiveEnd  func(ctx context.Context, token, text, turn string) error
}

// maxLiveWait is the longest GET /brain/live/next waits for an event.
const maxLiveWait = 60 * time.Second

// maxTurnEndBody is the most POST /brain/live/turn-end takes: an answer.
const maxTurnEndBody = 8 << 20

// LiveTurnEnd is the body of POST /brain/live/turn-end.
type LiveTurnEnd struct {
	Text string `json:"text"`
	// Turn is the "turn" of the last message the model read, "" when the
	// hook could not tell: the Stop then answers no turn.
	Turn string `json:"turn,omitempty"`
}

// brainLiveNext answers GET /brain/live/next?wait=SECONDS with the next
// event for the live session, or 204 when none came in time.
func (a *BrainAPI) brainLiveNext(w http.ResponseWriter, r *http.Request, _ *brainruntime.Brain) {
	wait := 25 * time.Second
	if s := r.URL.Query().Get("wait"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			WriteErr(w, http.StatusBadRequest, "wait is a number of seconds")
			return
		}
		wait = min(time.Duration(n)*time.Second, maxLiveWait)
	}
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	ev, ok, err := a.LiveNext(r.Context(), tok, wait)
	if err != nil {
		WriteErr(w, statusFor(apperr.KindOf(err)), err.Error())
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	WriteJSON(w, http.StatusOK, ev)
}

// brainLiveTurnEnd answers POST /brain/live/turn-end.
func (a *BrainAPI) brainLiveTurnEnd(w http.ResponseWriter, r *http.Request, _ *brainruntime.Brain) {
	var req LiveTurnEnd
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteErr(w, http.StatusBadRequest, `the body is {"text": "<the turn's last text>", "turn": "<the last turn read>"}`)
		return
	}
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if err := a.LiveEnd(r.Context(), tok, req.Text, req.Turn); err != nil {
		WriteErr(w, statusFor(apperr.KindOf(err)), err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "ok"})
}

// MCPCallRequest is the body of POST /brain/mcp/call.
type MCPCallRequest struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// RunHeader carries, on the MCP calls, the run id fridolin mcp was
// started with.
const RunHeader = "X-Fridolin-Run"

// brainMCPTools answers GET /brain/mcp/tools: {"tools": [...]}.
func (a *BrainAPI) brainMCPTools(w http.ResponseWriter, r *http.Request, _ *brainruntime.Brain) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	out, err := a.MCPTools(r.Context(), tok, r.Header.Get(RunHeader))
	if err != nil {
		WriteErr(w, statusFor(apperr.KindOf(err)), err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// brainMCPCall answers POST /brain/mcp/call with the tool's result, as
// MCP's tools/call has it: {"content": [...], "isError": …}.
func (a *BrainAPI) brainMCPCall(w http.ResponseWriter, r *http.Request, _ *brainruntime.Brain) {
	var req MCPCallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		WriteErr(w, http.StatusBadRequest, `the body is {"name": "<tool>", "arguments": {…}}`)
		return
	}
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	out, err := a.MCPCall(r.Context(), tok, r.Header.Get(RunHeader), req)
	if err != nil {
		WriteErr(w, statusFor(apperr.KindOf(err)), err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// brainCall authenticates a brain API call by the run's token and hands
// the brain it belongs to on to fn.
func (a *BrainAPI) brainCall(fn func(w http.ResponseWriter, r *http.Request, br *brainruntime.Brain)) http.HandlerFunc {
	return a.brainCallLimit(64<<10, fn)
}

// brainCallLimit is brainCall for a call whose body may be up to limit bytes.
func (a *BrainAPI) brainCallLimit(limit int64, fn func(w http.ResponseWriter, r *http.Request, br *brainruntime.Brain)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		br := a.BrainForToken(tok)
		if br == nil {
			WriteErr(w, http.StatusUnauthorized, "the bridge does not know this run's token (FRIDOLIN_BRAIN_TOKEN_FILE): it works only inside the run it was made for")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		fn(w, r, br)
	}
}

// brainPolls answers GET /brain/polls: ?id=MSG_ID for one poll, else the
// ?recent=N (default 10) newest, of ?chat=JID when given.
func (a *BrainAPI) brainPolls(w http.ResponseWriter, r *http.Request, br *brainruntime.Brain) {
	q := r.URL.Query()
	owns := func(chatJID string) bool { return a.OwnsChat(br, chatJID) }
	if id := q.Get("id"); id != "" {
		pollChat, p, err := a.ReadPoll(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !owns(pollChat)) {
			WriteErr(w, http.StatusNotFound, fmt.Sprintf("poll %q not found in this brain's chats", id))
			return
		}
		if err != nil {
			WriteErr(w, http.StatusInternalServerError, "reading the poll: "+err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, p)
		return
	}
	recent := 10
	if s := q.Get("recent"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			WriteErr(w, http.StatusBadRequest, "recent must be a number from 1 to 100")
			return
		}
		recent = n
	}
	chatJID := q.Get("chat")
	if chatJID != "" && !owns(chatJID) {
		WriteErr(w, http.StatusForbidden, fmt.Sprintf("chat %q is not one of this brain's chats", chatJID))
		return
	}
	polls, count, err := a.RecentPolls(r.Context(), chatJID, recent, owns)
	if err != nil {
		WriteErr(w, http.StatusInternalServerError, "reading the polls: "+err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, PollsResponse{Polls: polls, Count: count})
}

// callError is a brain API call the bridge refuses before doing anything,
// with the status to answer.
type callError struct {
	status int
	msg    string
}

func (e *callError) Error() string { return e.msg }

func badCall(format string, args ...any) error {
	return &callError{status: http.StatusBadRequest, msg: fmt.Sprintf(format, args...)}
}

// calendarHandler is one calendar call of a brain: cals are its calendars,
// and token gets its access token, refreshing it when it has expired.
type calendarHandler func(ctx context.Context, r *http.Request, cals google.Calendars, token func() (*google.Token, error)) (any, error)

// calendarCall runs fn for a brain's call and answers with its result. The
// brain's calendars come from its approved manifest, never from the call.
func (a *BrainAPI) calendarCall(fn calendarHandler) func(w http.ResponseWriter, r *http.Request, br *brainruntime.Brain) {
	return func(w http.ResponseWriter, r *http.Request, br *brainruntime.Brain) {
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		cals := google.CalendarsOf(br.Manifest())
		out, err := fn(ctx, r, cals, func() (*google.Token, error) { return a.Google().AccessToken(ctx, br.ID()) })
		if err != nil {
			if ce, ok := errors.AsType[*callError](err); ok {
				WriteErr(w, ce.status, ce.msg)
				return
			}
			// The grant's health hears what Google said (google.Note).
			a.Google().Note(br.ID(), err)
			status, msg := google.CallProblem(err)
			WriteErr(w, status, msg)
			return
		}
		a.Google().Note(br.ID(), nil)
		WriteJSON(w, http.StatusOK, out)
	}
}

// CalendarChoice is one calendar a brain may use, as `calendars` lists it.
type CalendarChoice struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Primary bool   `json:"primary"`
	Default bool   `json:"default"`
	Access  string `json:"access"`
}

// brainCalendarList answers GET /brain/calendar/calendars: the calendars
// of the account the brain may use.
func (a *BrainAPI) brainCalendarList(ctx context.Context, r *http.Request, cals google.Calendars, token func() (*google.Token, error)) (any, error) {
	tok, err := token()
	if err != nil {
		return nil, err
	}
	list, err := a.Google().CalendarList(ctx, tok)
	if err != nil {
		return nil, err
	}
	out := []CalendarChoice{}
	for _, e := range list {
		if ok, def := cals.Allows(e); ok {
			out = append(out, CalendarChoice{ID: e.ID, Name: e.Summary, Primary: e.Primary, Default: def, Access: e.AccessRole})
		}
	}
	// restricted: always, now that the bridge holds every brain to its
	// calendars; kept for what read calendar_tool.py's answer.
	return CalendarListResponse{Calendars: out, Restricted: true}, nil
}

// timeRange reads a call's from and to (RFC 3339) and its time zone.
func timeRange(from, to, tz string) (time.Time, time.Time, error) {
	start, err := time.Parse(time.RFC3339, from)
	if err != nil {
		return time.Time{}, time.Time{}, badCall("from must be a time such as 2026-01-05T00:00:00+01:00, got %q", from)
	}
	end, err := time.Parse(time.RFC3339, to)
	if err != nil {
		return time.Time{}, time.Time{}, badCall("to must be a time such as 2026-01-06T00:00:00+01:00, got %q", to)
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, badCall("to must come after from")
	}
	if tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return time.Time{}, time.Time{}, badCall("unknown timezone %q; use an IANA name such as Europe/Berlin", tz)
		}
	}
	return start, end, nil
}

// brainCalendarEvents answers GET /brain/calendar/events?from=&to=: the
// events of ?calendar=, else of every calendar of the brain, at most ?max=
// (default 100) in all, in start order.
func (a *BrainAPI) brainCalendarEvents(ctx context.Context, r *http.Request, cals google.Calendars, token func() (*google.Token, error)) (any, error) {
	q := r.URL.Query()
	from, to, err := timeRange(q.Get("from"), q.Get("to"), q.Get("time_zone"))
	if err != nil {
		return nil, err
	}
	limit := 100
	if s := q.Get("max"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil || limit < 1 || limit > 2500 {
			return nil, badCall("max must be a number from 1 to 2500")
		}
	}
	calendars, err := cals.Pick(q.Get("calendar"), true)
	if err != nil {
		return nil, err
	}
	tok, err := token()
	if err != nil {
		return nil, err
	}
	return a.Google().EventsOf(ctx, tok, calendars, from, to, q.Get("time_zone"), limit)
}

// brainSendUpdatesValues are what the Calendar API takes as sendUpdates.
var brainSendUpdatesValues = []string{"all", "externalOnly", "none"}

// CalendarWriteGuard is the policy a brain's calendar writes go through,
// as the bridge's tools that write do (docs/BRAINS.md, "Skills, risk and
// approvals"). tok is the run's token, which brainCall checked.
type CalendarWriteGuard interface {
	// Allow checks that the run may write: no lockdown, and a risk level
	// that allows writing. Its error says, for the run, why not.
	Allow(tok string) error
	// UseApproval checks that id names an approved request_approval the
	// run may take, not used yet, and uses it up. Its error says, for the
	// run, why not.
	UseApproval(ctx context.Context, tok, id string) error
	// Record writes a write's audit line.
	Record(tok string, w CalendarWrite)
}

// CalendarWrite is one calendar write as its audit line has it: never
// the event's title, description or attendees.
type CalendarWrite struct {
	// Action is create or delete.
	Action   string
	Calendar string
	// Event is the event's id, "" for a create that did not happen.
	Event string
	// Guests is how many attendees it reaches: a new event's, or the
	// other attendees of a deleted one.
	Guests int
	// SendUpdates is, for a new event with attendees, whether Google
	// emails them (send_updates).
	SendUpdates string
	// Approval is the approval_id that let it reach them, "" for none.
	Approval string
	// Outcome is ok, or why it was refused or failed.
	Outcome string
}

// calendarWrite is a write of the run tok stands for, on its way through
// the guard, until done or refused records it.
type calendarWrite struct {
	a   *BrainAPI
	tok string
	w   CalendarWrite
}

func (a *BrainAPI) calendarWrite(r *http.Request, action, calendar string) *calendarWrite {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return &calendarWrite{a: a, tok: tok, w: CalendarWrite{Action: action, Calendar: calendar}}
}

// allow is the guard's Allow.
func (c *calendarWrite) allow() error {
	if err := c.a.CalendarWrites.Allow(c.tok); err != nil {
		return c.refused(err.Error())
	}
	return nil
}

// approve takes the approval_id id of a write that reaches other people;
// ask says, for the run, how to ask for one.
func (c *calendarWrite) approve(ctx context.Context, id, ask string) error {
	if id == "" {
		return c.refused(ask)
	}
	if err := c.a.CalendarWrites.UseApproval(ctx, c.tok, id); err != nil {
		return c.refused(err.Error() + ". Do not take the step.")
	}
	c.w.Approval = id
	return nil
}

// refused records msg as why the write was refused, and is the call's
// answer.
func (c *calendarWrite) refused(msg string) error {
	c.w.Outcome = "refused: " + msg
	c.a.CalendarWrites.Record(c.tok, c.w)
	return &callError{status: http.StatusForbidden, msg: msg}
}

// done records how the write went, err for failed, and returns err.
func (c *calendarWrite) done(err error) error {
	c.w.Outcome = "ok"
	if err != nil {
		_, msg := google.CallProblem(err)
		c.w.Outcome = "failed: " + msg
	}
	c.a.CalendarWrites.Record(c.tok, c.w)
	return err
}

// brainCalendarCreate answers POST /brain/calendar/events: a NewEvent,
// into its calendar, else the brain's default one. An event Google emails
// its attendees about (send_updates other than none) sends something to
// other people, so it needs the owner's approval, as every such step does.
func (a *BrainAPI) brainCalendarCreate(ctx context.Context, r *http.Request, cals google.Calendars, token func() (*google.Token, error)) (any, error) {
	var ev google.NewEvent
	if err := decodeSingleJSON(r.Body, &ev); err != nil {
		return nil, badCall("the event is not valid JSON: %v", google.DecodeError(err))
	}
	body, err := ev.Body()
	if err != nil {
		return nil, badCall("%v", err)
	}
	if ev.SendUpdates == "" {
		ev.SendUpdates = "all"
	}
	if !slices.Contains(brainSendUpdatesValues, ev.SendUpdates) {
		return nil, badCall("send_updates must be one of %s", strings.Join(brainSendUpdatesValues, ", "))
	}
	calendars, err := cals.Pick(ev.Calendar, false)
	if err != nil {
		return nil, err
	}
	w := a.calendarWrite(r, "create", calendars[0])
	if err := w.allow(); err != nil {
		return nil, err
	}
	n := countGuests(ev.Attendees)
	if n > 0 {
		w.w.Guests, w.w.SendUpdates = n, ev.SendUpdates
	}
	if n > 0 && ev.SendUpdates != "none" {
		ask := fmt.Sprintf("Google emails this event's attendees an invitation (send_updates %s), which sends something to other people: that needs the owner's yes first. "+
			"Call request_approval with what you will send to whom, and once it answers approved, create the event again with its approval_id "+
			"(fridolin calendar create … --approval-id ID). If nobody is to be emailed, create it with --send-updates none instead.", ev.SendUpdates)
		if err := w.approve(ctx, ev.ApprovalID, ask); err != nil {
			return nil, err
		}
	} else if kind := audit.SecretIn(ev.Title + "\n" + ev.Description + "\n" + ev.Location); n > 0 && kind != "" {
		// Not emailed, but in the attendees' calendars all the same: a
		// secret-like value goes to them only with the owner's yes, as
		// from the tools that send text off the machine.
		ask := fmt.Sprintf("this event's text looks like a secret (%s), and its attendees see the event: do not send it. "+
			"If the owner wants exactly this sent, ask them with request_approval, saying what goes to whom without the value itself, "+
			"and create the event again with the approval_id it answers approved with (fridolin calendar create … --approval-id ID).", kind)
		if err := w.approve(ctx, ev.ApprovalID, ask); err != nil {
			return nil, err
		}
	}
	tok, err := token()
	if err != nil {
		return nil, w.done(err)
	}
	created, err := a.Google().InsertEvent(ctx, tok, calendars[0], body, ev.SendUpdates)
	w.w.Event = created.ID
	if err := w.done(err); err != nil {
		return nil, err
	}
	return CalendarCreatedResponse{Created: created}, nil
}

// countGuests is how many attendees a new event names.
func countGuests(attendees []string) int {
	n := 0
	for _, a := range attendees {
		if strings.TrimSpace(a) != "" {
			n++
		}
	}
	return n
}

// brainCalendarDelete answers DELETE /brain/calendar/events/{id}: in
// ?calendar=, else the brain's default calendar. An event with other
// attendees is in their calendars too, so deleting it needs the owner's
// approval (?approval_id=): the bridge asks Google for the event first.
func (a *BrainAPI) brainCalendarDelete(ctx context.Context, r *http.Request, cals google.Calendars, token func() (*google.Token, error)) (any, error) {
	id := r.PathValue("id")
	q := r.URL.Query()
	calendars, err := cals.Pick(q.Get("calendar"), false)
	if err != nil {
		return nil, err
	}
	w := a.calendarWrite(r, "delete", calendars[0])
	w.w.Event = id
	if err := w.allow(); err != nil {
		return nil, err
	}
	tok, err := token()
	if err != nil {
		return nil, w.done(err)
	}
	guests, err := a.Google().EventGuests(ctx, tok, calendars[0], id)
	if err != nil {
		return nil, w.done(err)
	}
	if guests > 0 {
		w.w.Guests = guests
		ask := "this event has other attendees, and deleting it takes it out of their calendars too: that needs the owner's yes first. " +
			"Call request_approval with what you will delete, and once it answers approved, delete it again with its approval_id " +
			"(fridolin calendar delete --event-id … --approval-id ID)."
		if err := w.approve(ctx, q.Get("approval_id"), ask); err != nil {
			return nil, err
		}
	}
	if err := w.done(a.Google().DeleteEvent(ctx, tok, calendars[0], id)); err != nil {
		return nil, err
	}
	return CalendarDeletedResponse{Deleted: id}, nil
}

// brainCalendarBusy answers POST /brain/calendar/freebusy {from, to,
// time_zone, calendar}: when the calendar, else any of the brain's
// calendars, is busy.
func (a *BrainAPI) brainCalendarBusy(ctx context.Context, r *http.Request, cals google.Calendars, token func() (*google.Token, error)) (any, error) {
	var req struct {
		From     string `json:"from"`
		To       string `json:"to"`
		TimeZone string `json:"time_zone"`
		Calendar string `json:"calendar"`
	}
	if err := decodeSingleJSON(r.Body, &req); err != nil {
		return nil, badCall("the request is not valid JSON: %v", err)
	}
	from, to, err := timeRange(req.From, req.To, req.TimeZone)
	if err != nil {
		return nil, err
	}
	calendars, err := cals.Pick(req.Calendar, true)
	if err != nil {
		return nil, err
	}
	tok, err := token()
	if err != nil {
		return nil, err
	}
	busy, err := a.Google().BusyTimes(ctx, tok, calendars, from, to, req.TimeZone)
	if err != nil {
		return nil, err
	}
	return CalendarBusyResponse{Busy: busy, Calendars: calendars}, nil
}
