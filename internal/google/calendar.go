package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

// The calendar calls of brains. A brain's Google token stays with the
// bridge, in a folder the sandbox denies: `fridolin calendar` asks the
// bridge (/brain/calendar/, brainapi.go), and the bridge makes the call
// with the token and only on the calendars ticked on the brain's page.
// Before, the calendar tool in the brain read the token itself and kept
// to those calendars by an environment variable the brain could change,
// and the token, refresh token and OAuth client secret included, was
// there for any program the brain ran.

// Calendars are the calendars of its Google account a brain may use.
type Calendars struct {
	allowed []string
	def     string // where new events go
}

// CalendarsOf reads them from the brain's approved manifest. A brain with
// no calendars chosen there (one connected by hand, or a brain.json edited
// since) may use its primary calendar only.
func CalendarsOf(m manifest.Manifest) Calendars {
	if m.Calendar == nil || len(m.Calendar.Calendars) == 0 {
		return Calendars{allowed: []string{"primary"}, def: "primary"}
	}
	c := Calendars{allowed: slices.Clone(m.Calendar.Calendars), def: m.Calendar.Default}
	if !slices.Contains(c.allowed, c.def) {
		c.def = c.allowed[0]
	}
	return c
}

// errCalendarNotAllowed is a call on a calendar the brain may not use.
var errCalendarNotAllowed = errors.New("calendar not enabled for this brain")

// Pick resolves the calendar a call names: none means every allowed
// calendar for a read (many) and the default one for a write, and
// "primary", the only name before calendars could be chosen, means the
// default one too.
func (c Calendars) Pick(requested string, many bool) ([]string, error) {
	switch {
	case requested == "" && many:
		return slices.Clone(c.allowed), nil
	case requested == "" || requested == "primary":
		return []string{c.def}, nil
	case slices.Contains(c.allowed, requested):
		return []string{requested}, nil
	}
	return nil, fmt.Errorf("%w: %q is not one of this brain's calendars, which are %s; the owner chooses them on the brain's page in the dashboard (Brains → Calendar)",
		errCalendarNotAllowed, requested, strings.Join(c.allowed, ", "))
}

// Allows reports whether an entry of the account's calendar list is one
// of the brain's, and whether it is the default.
func (c Calendars) Allows(e CalendarEntry) (allowed, isDefault bool) {
	is := func(id string) bool { return id == e.ID || (id == "primary" && e.Primary) }
	return slices.ContainsFunc(c.allowed, is), is(c.def)
}

// googleAPIError is an error answer of the Calendar API.
type googleAPIError struct {
	status  int
	message string
}

func (e *googleAPIError) Error() string {
	return fmt.Sprintf("Google Calendar %d: %s", e.status, strings.TrimSuffix(e.message, "."))
}

// hint is what a brain should do about e, or "".
func (e *googleAPIError) hint() string {
	switch e.status {
	case http.StatusUnauthorized:
		return calendarReconnectHint
	case http.StatusForbidden:
		return "If it says the access is insufficient, the brain's sign-in lacks it. " + calendarReconnectHint
	case http.StatusNotFound:
		return "Check the event and calendar IDs."
	}
	return ""
}

// calendarReconnectHint is what a brain tells its owner when Google no
// longer takes its sign-in.
const calendarReconnectHint = "Ask the owner to connect the calendar again in the dashboard: Brains → this brain → Calendar → Sign in again."

// calendarAPI calls the Calendar API with the brain's access token: method
// on path with query, body as JSON when it is not nil, and the answer
// decoded into out when it is not nil.
func (g *Auth) calendarAPI(ctx context.Context, tok *Token, method, path string, query url.Values, body, out any) error {
	u := g.options.Endpoints.APIBase + "/calendar/v3" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.options.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Google Calendar (%w); this is usually temporary, try again in a minute", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&e)
		if e.Error.Message == "" {
			e.Error.Message = http.StatusText(resp.StatusCode)
		}
		return &googleAPIError{status: resp.StatusCode, message: e.Error.Message}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out); err != nil {
		return fmt.Errorf("reading Google Calendar's answer: %w", DecodeError(err))
	}
	return nil
}

// maxGooglePages bounds a paged listing: Google may answer with empty
// pages that still carry a next-page token.
const maxGooglePages = 100

// googleEvent is the part of a Calendar API event the tools show.
type googleEvent struct {
	ID          string `json:"id"`
	Summary     string `json:"summary"`
	Location    string `json:"location"`
	Description string `json:"description"`
	Status      string `json:"status"`
	HTMLLink    string `json:"htmlLink"`
	Start       struct {
		DateTime string `json:"dateTime"`
		Date     string `json:"date"`
	} `json:"start"`
	End struct {
		DateTime string `json:"dateTime"`
		Date     string `json:"date"`
	} `json:"end"`
	Attendees []struct {
		Email string `json:"email"`
	} `json:"attendees"`
}

// CalendarEvent is an event as the calendar commands print it.
type CalendarEvent struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Start       string   `json:"start"`
	End         string   `json:"end"`
	Location    string   `json:"location,omitempty"`
	Description string   `json:"description,omitempty"`
	Attendees   []string `json:"attendees"`
	Status      string   `json:"status,omitempty"`
	HTMLLink    string   `json:"html_link,omitempty"`
	Calendar    string   `json:"calendar,omitempty"` // when the listing spans calendars
}

func (e googleEvent) brief() CalendarEvent {
	out := CalendarEvent{
		ID: e.ID, Title: e.Summary, Start: e.Start.DateTime, End: e.End.DateTime,
		Location: e.Location, Description: e.Description, Status: e.Status, HTMLLink: e.HTMLLink,
		Attendees: []string{},
	}
	if out.Title == "" {
		out.Title = "(no title)"
	}
	if out.Start == "" {
		out.Start = e.Start.Date
	}
	if out.End == "" {
		out.End = e.End.Date
	}
	for _, a := range e.Attendees {
		out.Attendees = append(out.Attendees, a.Email)
	}
	return out
}

// listEvents returns up to limit events of calendar between from and to,
// in start order, and whether it had more. Google may return fewer events
// per page than asked for even when more match, so it follows the pages:
// stopping early would make a busy day look free.
func (g *Auth) listEvents(ctx context.Context, tok *Token, calendar string, from, to time.Time, tz string, limit int) ([]googleEvent, bool, error) {
	q := url.Values{
		"timeMin":      {from.Format(time.RFC3339)},
		"timeMax":      {to.Format(time.RFC3339)},
		"singleEvents": {"true"},
		"orderBy":      {"startTime"},
		"maxResults":   {fmt.Sprint(min(limit, 2500))}, // the API's largest page
	}
	if tz != "" {
		q.Set("timeZone", tz)
	}
	var items []googleEvent
	for range maxGooglePages {
		var page struct {
			Items         []googleEvent `json:"items"`
			NextPageToken string        `json:"nextPageToken"`
		}
		if err := g.calendarAPI(ctx, tok, http.MethodGet, "/calendars/"+url.PathEscape(calendar)+"/events", q, nil, &page); err != nil {
			return nil, false, err
		}
		items = append(items, page.Items...)
		if len(items) >= limit {
			return items[:limit], len(items) > limit || page.NextPageToken != "", nil
		}
		if page.NextPageToken == "" {
			return items, false, nil
		}
		q.Set("pageToken", page.NextPageToken)
	}
	return items, true, nil
}

// CalendarListing is GET /brain/calendar/events: the events of the
// calendars asked for, merged in start order.
type CalendarListing struct {
	Events    []CalendarEvent `json:"events"`
	Count     int             `json:"count"`
	Truncated bool            `json:"truncated"`
}

// EventsOf lists the events of calendars, at most limit in all.
func (g *Auth) EventsOf(ctx context.Context, tok *Token, calendars []string, from, to time.Time, tz string, limit int) (*CalendarListing, error) {
	out := &CalendarListing{Events: []CalendarEvent{}}
	for _, cal := range calendars {
		items, more, err := g.listEvents(ctx, tok, cal, from, to, tz, limit)
		if err != nil {
			return nil, err
		}
		out.Truncated = out.Truncated || more
		for _, it := range items {
			e := it.brief()
			if len(calendars) > 1 {
				e.Calendar = cal
			}
			out.Events = append(out.Events, e)
		}
	}
	sort.SliceStable(out.Events, func(i, j int) bool { return eventStart(out.Events[i]) < eventStart(out.Events[j]) })
	if len(out.Events) > limit {
		out.Events, out.Truncated = out.Events[:limit], true
	}
	out.Count = len(out.Events)
	return out, nil
}

// eventStart is a key that sorts events by when they start: an all-day
// event at the start of its day. Every time is in the zone asked for, so
// the text sorts like the time.
func eventStart(e CalendarEvent) string {
	if len(e.Start) == len("2006-01-02") {
		return e.Start + "T00:00:00"
	}
	return e.Start
}

// NewEvent is POST /brain/calendar/events: an event to create.
type NewEvent struct {
	Calendar    string     `json:"calendar"`
	Title       string     `json:"title"`
	Start       string     `json:"start"` // RFC 3339, or a date for an all-day event
	End         string     `json:"end"`   // the same; an all-day event's end date is exclusive
	TimeZone    string     `json:"time_zone"`
	Description string     `json:"description"`
	Location    string     `json:"location"`
	Attendees   []string   `json:"attendees"`
	Reminders   []Reminder `json:"reminders"` // nil: one popup 10 minutes before
	SendUpdates string     `json:"send_updates"`
}

// Reminder is one of an event's reminders.
type Reminder struct {
	Method  string `json:"method"`
	Minutes int    `json:"minutes"`
}

// Body checks e and builds the Calendar API's event from it.
func (e NewEvent) Body() (map[string]any, error) {
	if strings.TrimSpace(e.Title) == "" {
		return nil, errors.New("the event needs a title")
	}
	var start, end map[string]any
	startDay, errStart := time.Parse(time.DateOnly, e.Start)
	endDay, errEnd := time.Parse(time.DateOnly, e.End)
	switch {
	case errStart == nil && errEnd == nil:
		if !endDay.After(startDay) {
			return nil, errors.New("an all-day event's end date is the day after its last day, so it must come after the start date")
		}
		start, end = map[string]any{"date": e.Start}, map[string]any{"date": e.End}
	case errStart == nil || errEnd == nil:
		return nil, errors.New("the start and the end must both be dates (an all-day event) or both times")
	default:
		s, err := time.Parse(time.RFC3339, e.Start)
		if err != nil {
			return nil, fmt.Errorf("start %q is not a time with an offset such as 2026-01-06T14:00:00+01:00", e.Start)
		}
		t, err := time.Parse(time.RFC3339, e.End)
		if err != nil {
			return nil, fmt.Errorf("end %q is not a time with an offset such as 2026-01-06T15:00:00+01:00", e.End)
		}
		if !t.After(s) {
			return nil, errors.New("the event must end after it starts")
		}
		start, end = map[string]any{"dateTime": e.Start}, map[string]any{"dateTime": e.End}
		if e.TimeZone != "" {
			if _, err := time.LoadLocation(e.TimeZone); err != nil {
				return nil, fmt.Errorf("unknown timezone %q; use an IANA name such as Europe/Berlin", e.TimeZone)
			}
			start["timeZone"], end["timeZone"] = e.TimeZone, e.TimeZone
		}
	}
	body := map[string]any{"summary": e.Title, "start": start, "end": end}
	if e.Description != "" {
		body["description"] = e.Description
	}
	if e.Location != "" {
		body["location"] = e.Location
	}
	var attendees []map[string]string
	for _, a := range e.Attendees {
		if a = strings.TrimSpace(a); a != "" {
			attendees = append(attendees, map[string]string{"email": a})
		}
	}
	if len(attendees) > 0 {
		body["attendees"] = attendees
	}
	reminders := e.Reminders
	if reminders == nil {
		reminders = []Reminder{{Method: "popup", Minutes: 10}}
	}
	for _, r := range reminders {
		if r.Method == "" || r.Minutes < 0 {
			return nil, fmt.Errorf("a reminder needs a method and minutes from 0 up, got %q and %d", r.Method, r.Minutes)
		}
	}
	body["reminders"] = map[string]any{"useDefault": false, "overrides": reminders}
	return body, nil
}

// sendUpdatesValues are what the Calendar API takes as sendUpdates.

// InsertEvent creates an event in calendar.
func (g *Auth) InsertEvent(ctx context.Context, tok *Token, calendar string, body map[string]any, sendUpdates string) (CalendarEvent, error) {
	var created googleEvent
	err := g.calendarAPI(ctx, tok, http.MethodPost, "/calendars/"+url.PathEscape(calendar)+"/events", url.Values{"sendUpdates": {sendUpdates}}, body, &created)
	return created.brief(), err
}

// DeleteEvent deletes event id of calendar.
func (g *Auth) DeleteEvent(ctx context.Context, tok *Token, calendar, id string) error {
	return g.calendarAPI(ctx, tok, http.MethodDelete, "/calendars/"+url.PathEscape(calendar)+"/events/"+url.PathEscape(id), nil, nil, nil)
}

// BusySlot is a time a calendar is busy.
type BusySlot struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// BusyTimes returns when any of calendars is busy between from and to. A
// calendar Google cannot read the times of is an error, not free time.
func (g *Auth) BusyTimes(ctx context.Context, tok *Token, calendars []string, from, to time.Time, tz string) ([]BusySlot, error) {
	req := map[string]any{"timeMin": from.Format(time.RFC3339), "timeMax": to.Format(time.RFC3339)}
	if tz != "" {
		req["timeZone"] = tz
	}
	var items []map[string]string
	for _, c := range calendars {
		items = append(items, map[string]string{"id": c})
	}
	req["items"] = items
	var resp struct {
		Calendars map[string]struct {
			Busy   []BusySlot `json:"busy"`
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"calendars"`
	}
	if err := g.calendarAPI(ctx, tok, http.MethodPost, "/freeBusy", nil, req, &resp); err != nil {
		return nil, err
	}
	busy := []BusySlot{}
	for _, c := range calendars {
		got, ok := resp.Calendars[c]
		if !ok {
			return nil, &busyError{calendar: c}
		}
		if len(got.Errors) > 0 {
			return nil, &busyError{calendar: c, reason: got.Errors[0].Reason}
		}
		busy = append(busy, got.Busy...)
	}
	return busy, nil
}

// busyError is a calendar Google's free/busy answer gives no busy times
// for, with the reason Google gives, if any: most often a calendar that
// was deleted or is no longer shared with the brain's account.
type busyError struct {
	calendar, reason string
}

func (e *busyError) Error() string {
	if e.reason == "" {
		return fmt.Sprintf("the answer from Google Calendar does not say when %s is busy", e.calendar)
	}
	return fmt.Sprintf("reading when %s is busy failed at Google Calendar (%s)", e.calendar, e.reason)
}

// calendarCheckHint is what a brain tells its owner about a calendar Google
// cannot read for it, where connecting again would not help.
const calendarCheckHint = "If it stays so, ask the owner to check that the calendar still exists and is shared with the brain's Google account, and which calendars the brain has: Brains → this brain → Calendar."

// CallProblem is the status and message a brain's calendar call
// gets for err, which came from getting the brain's token or from Google.
// Only a sign-in that Google refuses, or that the bridge cannot use, tells
// the brain to have its owner connect again: for anything else that would
// send the owner to fix what is not broken.
func CallProblem(err error) (int, string) {
	var se *signInError
	var oe *oauthError
	var ge *googleAPIError
	var be *busyError
	var ne net.Error
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return http.StatusPreconditionFailed, "Google Calendar is not connected for this brain. Ask the owner to connect it in the dashboard: Brains → this brain → Calendar → Connect Google Calendar."
	case errors.Is(err, errCalendarNotAllowed):
		return http.StatusForbidden, err.Error()
	case errors.As(err, &se):
		return http.StatusPreconditionFailed, "the brain's Google sign-in cannot be used (" + err.Error() + "). " + calendarReconnectHint
	case errors.As(err, &oe) && oe.temporary():
		return http.StatusServiceUnavailable, "Google could not refresh the calendar sign-in just now (" + oe.message + "). This is usually temporary; try again in a minute."
	case errors.As(err, &oe):
		return http.StatusPreconditionFailed, "Google refused to refresh the calendar sign-in (" + oe.message + "): it was revoked or has expired. " + calendarReconnectHint
	case errors.As(err, &ge):
		msg := err.Error()
		if h := ge.hint(); h != "" {
			msg += ". " + h
		}
		if ge.status == http.StatusNotFound {
			return http.StatusNotFound, msg
		}
		return http.StatusBadGateway, msg
	case errors.As(err, &be):
		return http.StatusBadGateway, err.Error() + ". " + calendarCheckHint
	case errors.As(err, &ne):
		return http.StatusServiceUnavailable, err.Error()
	}
	// Google's answer could not be read, say.
	return http.StatusBadGateway, err.Error()
}
