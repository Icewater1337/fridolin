package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
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
	DeleteEvent(context.Context, *google.Token, string, string) error
	BusyTimes(context.Context, *google.Token, []string, time.Time, time.Time, string) ([]google.BusySlot, error)
}
type BrainAPI struct {
	BrainForToken func(string) *brainruntime.Brain
	OwnsChat      func(*brainruntime.Brain, string) bool
	ReadPoll      func(context.Context, string) (string, any, error)
	RecentPolls   func(context.Context, string, int, func(string) bool) (any, int, error)
	Google        func() BrainCalendarService
}

// brainCall authenticates a brain API call by the run's token and hands
// the brain it belongs to on to fn.
func (a *BrainAPI) brainCall(fn func(w http.ResponseWriter, r *http.Request, br *brainruntime.Brain)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		br := a.BrainForToken(tok)
		if br == nil {
			WriteErr(w, http.StatusUnauthorized, "the bridge does not know this run's token (FRIDOLIN_BRAIN_TOKEN_FILE): it works only inside the run it was made for")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
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
			status, msg := google.CallProblem(err)
			WriteErr(w, status, msg)
			return
		}
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

// brainCalendarCreate answers POST /brain/calendar/events: a NewEvent,
// into its calendar, else the brain's default one.
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
	tok, err := token()
	if err != nil {
		return nil, err
	}
	created, err := a.Google().InsertEvent(ctx, tok, calendars[0], body, ev.SendUpdates)
	if err != nil {
		return nil, err
	}
	return CalendarCreatedResponse{Created: created}, nil
}

// brainCalendarDelete answers DELETE /brain/calendar/events/{id}: in
// ?calendar=, else the brain's default calendar.
func (a *BrainAPI) brainCalendarDelete(ctx context.Context, r *http.Request, cals google.Calendars, token func() (*google.Token, error)) (any, error) {
	id := r.PathValue("id")
	calendars, err := cals.Pick(r.URL.Query().Get("calendar"), false)
	if err != nil {
		return nil, err
	}
	tok, err := token()
	if err != nil {
		return nil, err
	}
	if err := a.Google().DeleteEvent(ctx, tok, calendars[0], id); err != nil {
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
