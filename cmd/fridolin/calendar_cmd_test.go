package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

// fakeCalendarAPI is the Calendar API of one account, for the calendar
// commands: a calendar list (one entry per page), events per calendar (two
// per page, in order), free/busy and the token endpoint. It records every
// calendar a request touched.
type fakeCalendarAPI struct {
	srv *httptest.Server

	mu       sync.Mutex
	events   map[string][]map[string]any // calendar → events
	busy     map[string][]BusySlot
	broken   map[string]bool // calendars free/busy reports an error for
	touched  []string        // "METHOD calendar" of every events or free/busy request
	inserted []map[string]any
	queries  []string
	refresh  int // token requests
	revoked  bool
}

func newFakeCalendarAPI(t *testing.T) *fakeCalendarAPI {
	t.Helper()
	f := &fakeCalendarAPI{events: map[string][]map[string]any{}, busy: map[string][]BusySlot{}, broken: map[string]bool{}}
	calendars := []map[string]any{
		{"id": "you@example.com", "summary": "you@example.com", "primary": true, "accessRole": "owner"},
		{"id": "team@example.com", "summary": "Team", "summaryOverride": "Our team", "accessRole": "writer"},
		{"id": "private@example.com", "summary": "Private", "accessRole": "owner"},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path != "/token" && r.Header.Get("Authorization") != "Bearer ACCESS-NEW" && r.Header.Get("Authorization") != "Bearer ACCESS-OK" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"Invalid Credentials"}}`)
			return
		}
		path := strings.TrimPrefix(r.URL.EscapedPath(), "/calendar/v3")
		page := func(n int) int { i, _ := strconv.Atoi(r.URL.Query().Get("pageToken")); return min(i, n) }
		switch {
		case r.URL.Path == "/token":
			f.refresh++
			fmt.Fprint(w, `{"access_token":"ACCESS-NEW","expires_in":3600}`)
		case path == "/users/me/calendarList":
			i := page(len(calendars))
			resp := map[string]any{"items": calendars[i:min(i+1, len(calendars))]}
			if i+1 < len(calendars) {
				resp["nextPageToken"] = strconv.Itoa(i + 1)
			}
			_ = json.NewEncoder(w).Encode(resp)
		case path == "/freeBusy":
			var req struct {
				Items []struct{ ID string } `json:"items"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			cals := map[string]any{}
			for _, it := range req.Items {
				f.touched = append(f.touched, "freebusy "+it.ID)
				if f.broken[it.ID] {
					cals[it.ID] = map[string]any{"errors": []map[string]string{{"domain": "global", "reason": "notFound"}}}
				} else {
					cals[it.ID] = map[string]any{"busy": f.busy[it.ID]}
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"calendars": cals})
		case strings.HasPrefix(path, "/calendars/"):
			rest := strings.TrimPrefix(path, "/calendars/")
			escaped, eventPath, _ := strings.Cut(rest, "/events")
			cal, _ := url.PathUnescape(escaped)
			f.touched = append(f.touched, r.Method+" "+cal)
			f.queries = append(f.queries, r.URL.RawQuery)
			switch {
			case r.Method == "GET":
				evs := f.events[cal]
				i := page(len(evs))
				resp := map[string]any{"items": evs[i:min(i+2, len(evs))]}
				if i+2 < len(evs) {
					resp["nextPageToken"] = strconv.Itoa(i + 2)
				}
				_ = json.NewEncoder(w).Encode(resp)
			case r.Method == "POST":
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				f.inserted = append(f.inserted, body)
				created := map[string]any{"id": "new1"}
				for k, v := range body {
					created[k] = v
				}
				_ = json.NewEncoder(w).Encode(created)
			case r.Method == "DELETE" && eventPath == "/gone":
				w.WriteHeader(404)
				fmt.Fprint(w, `{"error":{"message":"Not Found"}}`)
			case r.Method == "DELETE":
				w.WriteHeader(204)
			}
		case r.URL.Path == "/revoke":
			f.revoked = true
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	prev := [3]string{googleAPIBase, googleTokenEndpoint, googleRevokeEndpoint}
	googleAPIBase, googleTokenEndpoint, googleRevokeEndpoint = f.srv.URL, f.srv.URL+"/token", f.srv.URL+"/revoke"
	t.Cleanup(func() { googleAPIBase, googleTokenEndpoint, googleRevokeEndpoint = prev[0], prev[1], prev[2] })
	return f
}

func (f *fakeCalendarAPI) touchedCalendars() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.touched)
}

// calendarBrain sets up brain a with a Google token (expired when stale)
// and, unless cals is nil, the calendars ticked on its page.
func calendarBrain(t *testing.T, f *fakeCalendarAPI, cals []string, def string, stale bool) (*testBridge, string, string) {
	t.Helper()
	tb, url := serveTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A", Routing: manifest.Routing{HandlesDMs: true}})
	if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, f.srv.URL)}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	tok := googleToken{
		Token: "ACCESS-OK", RefreshToken: "RT-1", ClientID: "123-abc.apps.googleusercontent.com",
		Scopes: calendarScopes, Expiry: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
	}
	if stale {
		tok.Token, tok.Expiry = "ACCESS-OLD", "2020-01-01T00:00:00.000000Z"
	}
	data, _ := json.Marshal(tok)
	if err := os.WriteFile(tb.google.tokenPath("a"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if cals != nil {
		if w := tb.do("PUT", "/api/brains/a/calendar", map[string]any{"account": "you@example.com", "calendars": cals, "default": def}); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	token, revoke := tb.runTokens.mint("a")
	t.Cleanup(revoke)
	return tb, url, token
}

func calendarEvent(id, title, start, end string) map[string]any {
	key := "dateTime"
	if len(start) == len("2006-01-02") {
		key = "date"
	}
	return map[string]any{"id": id, "summary": title, "start": map[string]string{key: start}, "end": map[string]string{key: end},
		"attendees": []map[string]string{{"email": "alex@example.com"}}, "status": "confirmed"}
}

func eventTitles(doc map[string]any) []string {
	var out []string
	evs, _ := doc["events"].([]any)
	for _, e := range evs {
		m := e.(map[string]any)
		title := m["title"].(string)
		if c, ok := m["calendar"].(string); ok {
			title += "@" + strings.Split(c, "@")[0]
		}
		out = append(out, title)
	}
	return out
}

// TestCalendarListThroughTheBridge: list merges the brain's calendars in
// start order, follows Google's pages and asks for the whole of the days,
// in the brain's timezone, and never touches a calendar the brain may not
// use.
func TestCalendarListThroughTheBridge(t *testing.T) {
	f := newFakeCalendarAPI(t)
	f.events["you@example.com"] = []map[string]any{
		calendarEvent("1", "Dentist", "2026-01-05T09:00:00+01:00", "2026-01-05T10:00:00+01:00"),
		calendarEvent("2", "Lunch", "2026-01-05T12:00:00+01:00", "2026-01-05T13:00:00+01:00"),
		calendarEvent("3", "Gym", "2026-01-06T18:00:00+01:00", "2026-01-06T19:00:00+01:00"),
	}
	f.events["team@example.com"] = []map[string]any{
		calendarEvent("4", "Offsite", "2026-01-05", "2026-01-06"),
		calendarEvent("5", "Standup", "2026-01-05T09:30:00+01:00", "2026-01-05T09:45:00+01:00"),
	}
	f.events["private@example.com"] = []map[string]any{calendarEvent("6", "Secret", "2026-01-05T08:00:00+01:00", "2026-01-05T09:00:00+01:00")}
	_, url, token := calendarBrain(t, f, []string{"you@example.com", "team@example.com"}, "team@example.com", true)

	cases := []struct {
		name string
		args []string
		want []string
		more bool
	}{
		{"every calendar of the brain", []string{"list", "--from", "2026-01-05", "--to", "2026-01-06"},
			[]string{"Offsite@team", "Dentist@you", "Standup@team", "Lunch@you", "Gym@you"}, false},
		{"one calendar", []string{"--calendar-id", "you@example.com", "list", "--from", "2026-01-05"}, []string{"Dentist", "Lunch", "Gym"}, false},
		{"the flag after the command", []string{"list", "--calendar-id", "team@example.com", "--from", "2026-01-05"}, []string{"Offsite", "Standup"}, false},
		{"primary is the default", []string{"list", "--calendar-id", "primary", "--from", "2026-01-05"}, []string{"Offsite", "Standup"}, false},
		{"at most", []string{"list", "--from", "2026-01-05", "--max-results", "2"}, []string{"Offsite@team", "Dentist@you"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, doc := brainTool(t, calendarCmd, url, token, tc.args...)
			if got := eventTitles(doc); code != 0 || strings.Join(got, "|") != strings.Join(tc.want, "|") || doc["truncated"] != tc.more {
				t.Errorf("exit %d, events %q truncated %v; want %q truncated %v\n%v", code, got, doc["truncated"], tc.want, tc.more, doc)
			}
		})
	}
	code, doc := brainTool(t, calendarCmd, url, token, "list", "--from", "2026-01-05", "--calendar-id", "you@example.com")
	evs := doc["events"].([]any)
	first := evs[0].(map[string]any)
	if code != 0 || first["id"] != "1" || first["start"] != "2026-01-05T09:00:00+01:00" || fmt.Sprint(first["attendees"]) != "[alex@example.com]" || first["status"] != "confirmed" {
		t.Errorf("event = %v", first)
	}
	q := f.queries[len(f.queries)-1]
	for _, want := range []string{"timeMin=2026-01-05T00%3A00%3A00%2B01%3A00", "timeMax=2026-01-06T00%3A00%3A00%2B01%3A00", "timeZone=Europe%2FZurich", "singleEvents=true", "orderBy=startTime"} {
		if !strings.Contains(q, want) {
			t.Errorf("events query %q lacks %s", q, want)
		}
	}
	for _, c := range f.touchedCalendars() {
		if strings.Contains(c, "private@") {
			t.Errorf("the bridge read a calendar the brain may not use: %s", c)
		}
	}
	if f.refresh != 1 {
		t.Errorf("the expired token was refreshed %d times, want once and then reused", f.refresh)
	}
}

// TestCalendarIsHeldToTheBrainsCalendars: the bridge, not the brain,
// decides which calendars a call may touch: it goes by the brain's
// approved manifest, so another calendar of the account is refused before
// Google is asked, whatever the call names.
func TestCalendarIsHeldToTheBrainsCalendars(t *testing.T) {
	f := newFakeCalendarAPI(t)
	_, url, token := calendarBrain(t, f, []string{"you@example.com", "team@example.com"}, "team@example.com", false)
	for _, args := range [][]string{
		{"--calendar-id", "private@example.com", "list", "--from", "2026-01-05"},
		{"--calendar-id", "private@example.com", "create", "--title", "x", "--start", "2026-01-05T10:00", "--end", "2026-01-05T11:00"},
		{"--calendar-id", "private@example.com", "delete", "--event-id", "e1"},
		{"--calendar-id", "private@example.com", "free", "--date", "2026-01-05"},
	} {
		t.Run(args[2], func(t *testing.T) {
			code, doc := brainTool(t, calendarCmd, url, token, args...)
			if msg, _ := doc["error"].(string); code != 1 || !strings.Contains(msg, "not one of this brain's calendars") || !strings.Contains(msg, "you@example.com, team@example.com") {
				t.Errorf("exit %d, %v; want the calendar refused", code, doc)
			}
		})
	}
	if got := f.touchedCalendars(); len(got) != 0 {
		t.Errorf("Google was asked about %v", got)
	}

	t.Run("calendars lists only the brain's", func(t *testing.T) {
		code, doc := brainTool(t, calendarCmd, url, token, "calendars")
		data, _ := json.Marshal(doc["calendars"])
		want := `[{"access":"owner","default":false,"id":"you@example.com","name":"you@example.com","primary":true},{"access":"writer","default":true,"id":"team@example.com","name":"Our team","primary":false}]`
		if code != 0 || string(data) != want || doc["restricted"] != true {
			t.Errorf("exit %d\ngot  %s\nwant %s", code, data, want)
		}
	})
}

// TestCalendarDefaultIsOneOfTheBrainsCalendars: the dashboard keeps a
// brain's default calendar among the ticked ones, but brain.json does
// not: a brain can edit it, and the owner may approve the edit. A default
// that is not ticked, or none, falls back to the first ticked calendar,
// so new events and deletes never reach a calendar the brain may not use.
func TestCalendarDefaultIsOneOfTheBrainsCalendars(t *testing.T) {
	for _, def := range []string{"private@example.com", ""} {
		t.Run(fmt.Sprintf("default %q", def), func(t *testing.T) {
			f := newFakeCalendarAPI(t)
			tb, url, token := calendarBrain(t, f, []string{"you@example.com"}, "you@example.com", false)
			dir := tb.brainMgr.Get("a").Dir()
			m, err := manifest.Read(dir)
			if err != nil {
				t.Fatal(err)
			}
			m.Calendar.Default = def
			if err := manifest.Write(dir, m, true); err != nil {
				t.Fatal(err)
			}
			if w := tb.do("POST", "/api/brains/a/approve", map[string]any{"manifest": m}); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			if got := tb.brainMgr.Get("a").settings.Load().manifest.Calendar; got.Default != def {
				t.Fatalf("the approved manifest has default %q, want %q", got.Default, def)
			}

			if code, doc := brainTool(t, calendarCmd, url, token, "create", "--title", "x", "--start", "2026-01-05T10:00", "--end", "2026-01-05T11:00"); code != 0 {
				t.Errorf("create: exit %d, %v", code, doc)
			}
			if code, doc := brainTool(t, calendarCmd, url, token, "delete", "--event-id", "e1"); code != 0 {
				t.Errorf("delete: exit %d, %v", code, doc)
			}
			if got := f.touchedCalendars(); strings.Join(got, ",") != "POST you@example.com,DELETE you@example.com" {
				t.Errorf("touched %v, want the ticked calendar only", got)
			}
			code, doc := brainTool(t, calendarCmd, url, token, "calendars")
			if data, _ := json.Marshal(doc["calendars"]); code != 0 || !strings.Contains(string(data), `"default":true,"id":"you@example.com"`) {
				t.Errorf("exit %d, calendars %s; want you@example.com as the default", code, data)
			}
		})
	}
}

// TestCalendarWithNoCalendarsChosen: a brain whose brain.json has no
// calendars (one connected by hand) gets its primary calendar only.
func TestCalendarWithNoCalendarsChosen(t *testing.T) {
	f := newFakeCalendarAPI(t)
	tb, url, token := calendarBrain(t, f, nil, "", false)
	if tb.brainMgr.Get("a").settings.Load().manifest.Calendar != nil {
		t.Fatal("the brain has calendars chosen")
	}
	code, doc := brainTool(t, calendarCmd, url, token, "calendars")
	if data, _ := json.Marshal(doc["calendars"]); code != 0 || !strings.Contains(string(data), `"id":"you@example.com"`) || strings.Contains(string(data), "team@") {
		t.Errorf("exit %d, calendars %s; want the primary one only", code, data)
	}
	if code, doc := brainTool(t, calendarCmd, url, token, "--calendar-id", "team@example.com", "list"); code != 1 {
		t.Errorf("exit %d, %v; want another calendar refused", code, doc)
	}
	if code, _ := brainTool(t, calendarCmd, url, token, "list", "--from", "2026-01-05"); code != 0 || f.touchedCalendars()[0] != "GET primary" {
		t.Errorf("exit %d, touched %v; want the primary calendar read", code, f.touchedCalendars())
	}
}

func TestCalendarCreate(t *testing.T) {
	f := newFakeCalendarAPI(t)
	tb, url, token := calendarBrain(t, f, []string{"you@example.com", "team@example.com"}, "team@example.com", false)
	cases := []struct {
		name     string
		args     []string
		calendar string
		want     string // the event body sent to Google, as JSON
		query    string
	}{
		{"a meeting, into the default calendar", []string{"create", "--title", "Design review", "--start", "2026-01-06T14:00:00", "--end", "2026-01-06T15:00", "--attendees", "alex@example.com, sam@example.com,", "--description", "notes", "--location", "Room 1"},
			"team@example.com",
			`{"attendees":[{"email":"alex@example.com"},{"email":"sam@example.com"}],"description":"notes","end":{"dateTime":"2026-01-06T15:00:00+01:00","timeZone":"Europe/Zurich"},"location":"Room 1","reminders":{"overrides":[{"method":"popup","minutes":10}],"useDefault":false},"start":{"dateTime":"2026-01-06T14:00:00+01:00","timeZone":"Europe/Zurich"},"summary":"Design review"}`,
			"sendUpdates=all"},
		{"all day, in another calendar, quietly", []string{"--calendar-id", "you@example.com", "create", "--title", "Holiday", "--start", "2026-07-01", "--end", "2026-07-03", "--reminders", "email:60, popup:0", "--send-updates", "none"},
			"you@example.com",
			`{"end":{"date":"2026-07-03"},"reminders":{"overrides":[{"method":"email","minutes":60},{"method":"popup","minutes":0}],"useDefault":false},"start":{"date":"2026-07-01"},"summary":"Holiday"}`,
			"sendUpdates=none"},
		{"a time with its own offset, in summer time", []string{"create", "--title", "Call", "--start", "2026-07-01T09:00:00Z", "--end", "2026-07-01T11:30"},
			"team@example.com",
			`{"end":{"dateTime":"2026-07-01T11:30:00+02:00","timeZone":"Europe/Zurich"},"reminders":{"overrides":[{"method":"popup","minutes":10}],"useDefault":false},"start":{"dateTime":"2026-07-01T09:00:00Z","timeZone":"Europe/Zurich"},"summary":"Call"}`,
			"sendUpdates=all"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, doc := brainTool(t, calendarCmd, url, token, tc.args...)
			created, _ := doc["created"].(map[string]any)
			if code != 0 || created["id"] != "new1" {
				t.Fatalf("exit %d, %v", code, doc)
			}
			f.mu.Lock()
			body, _ := json.Marshal(f.inserted[len(f.inserted)-1])
			touched, query := f.touched[len(f.touched)-1], f.queries[len(f.queries)-1]
			f.mu.Unlock()
			if string(body) != tc.want || touched != "POST "+tc.calendar || query != tc.query {
				t.Errorf("Google got %s %s ?%s\n%s\nwant %s ?%s\n%s", touched, "", query, body, tc.calendar, tc.query, tc.want)
			}
		})
	}

	// The command always says whether Google emails the attendees; a call
	// a brain's own script makes may not, and gets what the command's
	// --send-updates defaults to.
	t.Run("a call without send_updates", func(t *testing.T) {
		ev := map[string]any{"title": "Call", "start": "2026-01-06T14:00:00+01:00", "end": "2026-01-06T15:00:00+01:00", "attendees": []string{"alex@example.com"}}
		w := tb.do("POST", "/brain/calendar/events", ev, func(r *http.Request) {
			r.Header.Del("X-Bridge-Token")
			r.Header.Set("Authorization", "Bearer "+token)
		})
		f.mu.Lock()
		touched, query := f.touched[len(f.touched)-1], f.queries[len(f.queries)-1]
		f.mu.Unlock()
		if w.Code != 200 || touched != "POST team@example.com" || query != "sendUpdates=all" {
			t.Errorf("%d %s; Google got %s ?%s, want POST team@example.com ?sendUpdates=all", w.Code, w.Body.String(), touched, query)
		}
	})
}

// TestCalendarRefusesBadCalls: a mistake is an error the assistant can act
// on, before anything reaches Google.
func TestCalendarRefusesBadCalls(t *testing.T) {
	f := newFakeCalendarAPI(t)
	_, url, token := calendarBrain(t, f, []string{"you@example.com"}, "you@example.com", false)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no command", nil, "give a command"},
		{"unknown command", []string{"agenda"}, `unknown command "agenda"`},
		{"auth", []string{"auth"}, "connected in the dashboard"},
		{"no title", []string{"create", "--start", "2026-01-06T14:00", "--end", "2026-01-06T15:00"}, "needs --title"},
		{"bad time", []string{"create", "--title", "x", "--start", "tomorrow at 3", "--end", "2026-01-06T15:00"}, `cannot read "tomorrow at 3": use YYYY-MM-DDTHH:MM:SS, or YYYY-MM-DD`},
		{"bad clock time", []string{"create", "--title", "x", "--start", "2026-01-06T25:00", "--end", "2026-01-06T15:00"}, `cannot read "2026-01-06T25:00"`},
		{"date and time mixed", []string{"create", "--title", "x", "--start", "2026-01-06", "--end", "2026-01-06T15:00"}, "both be dates"},
		{"ends before it starts", []string{"create", "--title", "x", "--start", "2026-01-06T15:00", "--end", "2026-01-06T14:00"}, "end after it starts"},
		{"all day, same day", []string{"create", "--title", "x", "--start", "2026-01-06", "--end", "2026-01-06"}, "day after its last day"},
		{"bad reminder", []string{"create", "--title", "x", "--start", "2026-01-06T14:00", "--end", "2026-01-06T15:00", "--reminders", "popup"}, "bad --reminders entry"},
		{"bad send-updates", []string{"create", "--title", "x", "--start", "2026-01-06T14:00", "--end", "2026-01-06T15:00", "--send-updates", "some"}, "--send-updates must be one of"},
		{"bad date", []string{"list", "--from", "05.01.2026"}, "use YYYY-MM-DD"},
		{"to before from", []string{"list", "--from", "2026-01-06", "--to", "2026-01-05"}, "comes before"},
		{"no max", []string{"list", "--max-results", "0"}, "at least 1"},
		{"no date", []string{"free"}, "needs --date"},
		{"bad hours", []string{"free", "--date", "2026-01-05", "--hours", "8-18"}, "use HH:MM-HH:MM"},
		{"hours past midnight", []string{"free", "--date", "2026-01-05", "--hours", "08:00-24:30"}, "00:00 to 24:00"},
		{"hours backwards", []string{"free", "--date", "2026-01-05", "--hours", "18:00-08:00"}, "start must come before"},
		{"no event id", []string{"delete"}, "needs --event-id"},
		{"unknown timezone", []string{"--timezone", "Mars/Olympus", "list"}, `unknown timezone "Mars/Olympus"`},
		{"stray argument", []string{"list", "today"}, `unexpected argument "today"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, doc := brainTool(t, calendarCmd, url, token, tc.args...)
			if msg, _ := doc["error"].(string); code != 1 || !strings.Contains(msg, tc.want) {
				t.Errorf("exit %d, %v; want an error with %q", code, doc, tc.want)
			}
		})
	}
	if got := f.touchedCalendars(); len(got) != 0 {
		t.Errorf("Google was asked: %v", got)
	}
}

func TestCalendarDelete(t *testing.T) {
	f := newFakeCalendarAPI(t)
	_, url, token := calendarBrain(t, f, []string{"you@example.com", "team@example.com"}, "team@example.com", false)
	if code, doc := brainTool(t, calendarCmd, url, token, "delete", "--event-id", "e1"); code != 0 || doc["deleted"] != "e1" {
		t.Errorf("exit %d, %v", code, doc)
	}
	if got := f.touchedCalendars(); len(got) != 1 || got[0] != "DELETE team@example.com" {
		t.Errorf("touched %v, want the default calendar", got)
	}
	code, doc := brainTool(t, calendarCmd, url, token, "delete", "--event-id", "gone")
	if msg, _ := doc["error"].(string); code != 1 || msg != "Google Calendar 404: Not Found. Check the event and calendar IDs." {
		t.Errorf("exit %d, %v", code, doc)
	}
}

func TestCalendarFree(t *testing.T) {
	f := newFakeCalendarAPI(t)
	f.busy["you@example.com"] = []BusySlot{
		{"2026-01-05T08:30:00Z", "2026-01-05T09:30:00Z"}, // 09:30-10:30 in Zurich
		{"2026-01-05T18:00:00Z", "2026-01-05T19:00:00Z"}, // 19:00-20:00, outside the hours
	}
	f.busy["team@example.com"] = []BusySlot{{"2026-01-05T06:00:00Z", "2026-01-05T07:15:00Z"}} // 07:00-08:15, half outside
	_, url, token := calendarBrain(t, f, []string{"you@example.com", "team@example.com"}, "team@example.com", false)

	code, doc := brainTool(t, calendarCmd, url, token, "free", "--date", "2026-01-05", "--min-slot", "20")
	data, _ := json.Marshal(doc)
	want := `{"busy":[{"end":"10:30","start":"09:30"},{"end":"20:00","start":"19:00"},{"end":"08:15","start":"07:00"}],"date":"2026-01-05","free_windows":[{"duration_minutes":75,"end":"09:30","start":"08:15"},{"duration_minutes":450,"end":"18:00","start":"10:30"}],"hours":"08:00-18:00","timezone":"Europe/Zurich"}`
	if code != 0 || string(data) != want {
		t.Errorf("exit %d\ngot  %s\nwant %s", code, data, want)
	}
	if got := f.touchedCalendars(); strings.Join(got, ",") != "freebusy you@example.com,freebusy team@example.com" {
		t.Errorf("touched %v", got)
	}

	// A calendar Google cannot read the busy times of is not free.
	f.mu.Lock()
	f.broken["team@example.com"] = true
	f.mu.Unlock()
	code, doc = brainTool(t, calendarCmd, url, token, "free", "--date", "2026-01-05")
	if msg, _ := doc["error"].(string); code != 1 || !strings.Contains(msg, "team@example.com") || !strings.Contains(msg, "notFound") {
		t.Errorf("exit %d, %v; want the unreadable calendar named", code, doc)
	}
	// Connecting again would not bring back a deleted or unshared calendar.
	if msg, _ := doc["error"].(string); strings.Contains(msg, calendarReconnectHint) || strings.Contains(msg, "sign-in") || !strings.Contains(msg, calendarCheckHint) {
		t.Errorf("error %q: want the hint to check the brain's calendars, not to sign in again", msg)
	}
}

// TestCalendarCallProblems: what a calendar call answers for each kind of
// error. Only a sign-in Google refuses, or one the bridge cannot use, says
// to connect again.
func TestCalendarCallProblems(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		err       error
		status    int
		reconnect bool
		want      string
	}{
		{"not connected", &fs.PathError{Op: "open", Path: "token.json", Err: fs.ErrNotExist}, 412, false, "not connected for this brain"},
		{"a token the bridge cannot use", &signInError{errors.New("the token has no refresh token — connect again")}, 412, true, "cannot be used (the token has no refresh token"},
		{"an unreadable token file", &signInError{fmt.Errorf("unreadable token file: %w", errors.New("bad JSON"))}, 412, true, "unreadable token file"},
		{"revoked", &oauthError{status: 400, message: "invalid_grant"}, 412, true, "revoked or has expired"},
		{"Google's token endpoint is down", &oauthError{status: 503, message: "backend_error"}, 503, false, "usually temporary"},
		{"Google refuses the call", &googleAPIError{status: 401, message: "Invalid Credentials"}, 502, true, "Google Calendar 401"},
		{"no such event", &googleAPIError{status: 404, message: "Not Found"}, 404, false, "Check the event and calendar IDs"},
		{"a calendar without busy times", &busyError{calendar: "team@example.com", reason: "notFound"}, 502, false, calendarCheckHint},
		{"a calendar missing from the answer", &busyError{calendar: "team@example.com"}, 502, false, "does not say when team@example.com is busy"},
		{"an answer that cannot be read", fmt.Errorf("reading Google Calendar's answer: %w", errors.New("unexpected EOF")), 502, false, "reading Google Calendar's answer"},
		{"Google unreachable", fmt.Errorf("could not reach Google Calendar (%w)", &net.DNSError{Err: "no such host", IsTemporary: true}), 503, false, "could not reach"},
		{"another calendar", fmt.Errorf("%w: %q", errCalendarNotAllowed, "x@example.com"), 403, false, "not enabled for this brain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, msg := calendarCallProblem(tc.err)
			if status != tc.status || !strings.Contains(msg, tc.want) || strings.Contains(msg, calendarReconnectHint) != tc.reconnect {
				t.Errorf("%d %q; want %d with %q, reconnect hint %v", status, msg, tc.status, tc.want, tc.reconnect)
			}
		})
	}
}

func TestFreeWindows(t *testing.T) {
	t.Parallel()
	loc, _ := time.LoadLocation("Europe/Zurich")
	at := func(day, h, m int) time.Time { return time.Date(2026, 3, day, h, m, 0, 0, loc) }
	cases := []struct {
		name       string
		busy       [][2]time.Time
		start, end time.Time
		minSlot    int
		want       string
	}{
		{"free all day", nil, at(5, 8, 0), at(5, 18, 0), 30, "08:00-18:00/600"},
		{"overlapping and unsorted", [][2]time.Time{{at(5, 11, 0), at(5, 12, 0)}, {at(5, 9, 0), at(5, 10, 0)}, {at(5, 9, 30), at(5, 11, 30)}}, at(5, 8, 0), at(5, 18, 0), 30, "08:00-09:00/60 12:00-18:00/360"},
		{"short gaps left out", [][2]time.Time{{at(5, 8, 0), at(5, 9, 0)}, {at(5, 9, 15), at(5, 18, 0)}}, at(5, 8, 0), at(5, 18, 0), 30, ""},
		{"clipped to the window", [][2]time.Time{{at(5, 7, 0), at(5, 8, 30)}, {at(5, 17, 30), at(5, 19, 0)}}, at(5, 8, 0), at(5, 18, 0), 30, "08:30-17:30/540"},
		{"to midnight", nil, at(5, 20, 0), at(6, 0, 0), 30, "20:00-24:00/240"},
		// 29 March 2026: the clocks go from 02:00 to 03:00, so the day has
		// 23 hours; 00:00-24:00 is 1380 minutes.
		{"the day the clocks change", nil, at(29, 0, 0), at(30, 0, 0), 30, "00:00-24:00/1380"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var busy []timeRangeOf
			for _, b := range tc.busy {
				busy = append(busy, timeRangeOf{b[0], b[1]})
			}
			var got []string
			for _, w := range freeWindows(busy, tc.start, tc.end, tc.minSlot) {
				got = append(got, fmt.Sprintf("%s-%s/%d", w.Start, w.End, w.Minutes))
			}
			if strings.Join(got, " ") != tc.want {
				t.Errorf("free windows = %q, want %q", strings.Join(got, " "), tc.want)
			}
		})
	}
}

// TestCalendarZone: the timezone comes from --timezone, CALENDAR_TZ, TZ,
// then the zoneinfo file /etc/localtime links to, and a missing one is an
// error rather than a guess.
func TestCalendarZone(t *testing.T) {
	dir := t.TempDir()
	zoneinfo := dir + "/usr/share/zoneinfo/Asia"
	if err := os.MkdirAll(zoneinfo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zoneinfo+"/Tokyo", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := dir + "/localtime"
	if err := os.Symlink(zoneinfo+"/Tokyo", link); err != nil {
		t.Fatal(err)
	}
	prev := zoneinfoLink
	t.Cleanup(func() { zoneinfoLink = prev })
	cases := []struct {
		name, flag string
		env        map[string]string
		link       string
		want       string
	}{
		{"the flag", "America/New_York", map[string]string{"CALENDAR_TZ": "Europe/Zurich"}, link, "America/New_York"},
		{"CALENDAR_TZ", "", map[string]string{"CALENDAR_TZ": "Europe/Zurich", "TZ": "Asia/Kolkata"}, link, "Europe/Zurich"},
		{"TZ", "", map[string]string{"TZ": ":Asia/Kolkata"}, link, "Asia/Kolkata"},
		{"the system's", "", nil, link, "Asia/Tokyo"},
		{"none", "", nil, dir + "/missing", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			zoneinfoLink = tc.link
			loc, err := calendarZone(tc.flag, toolIO{getenv: func(k string) string { return tc.env[k] }, stderr: io.Discard})
			switch {
			case tc.want == "" && (err == nil || !strings.Contains(err.Error(), "CALENDAR_TZ")):
				t.Errorf("zone = %v, %v; want an error naming CALENDAR_TZ", loc, err)
			case tc.want != "" && (err != nil || loc.String() != tc.want):
				t.Errorf("zone = %v, %v; want %s", loc, err, tc.want)
			}
		})
	}
}

// TestCalendarSignInProblems: a brain whose sign-in Google refuses, that
// the bridge will not use, or that was never connected, is told to have the
// owner connect it, and a Google outage is not mistaken for that. Every way
// accessToken refuses a token goes through the bridge here, since only the
// type of its error tells calendarCallProblem which of these it is: the
// command prints the message, and the call it makes answers the status.
func TestCalendarSignInProblems(t *testing.T) {
	// Expired, so a call has to refresh it first.
	expired := googleToken{
		Token: "ACCESS-OLD", RefreshToken: "RT-1", ClientID: "123-abc.apps.googleusercontent.com",
		Scopes: calendarScopes, Expiry: "2020-01-01T00:00:00.000000Z",
	}
	writeToken := func(t *testing.T, path string, tok googleToken) {
		t.Helper()
		data, _ := json.Marshal(tok)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name      string
		setup     func(t *testing.T, tb *testBridge, path string)
		token     int    // the token endpoint's status; 0: the fake's, which grants it
		body      string // and its answer
		status    int
		reconnect bool
		want      string
	}{
		{"never connected", func(t *testing.T, _ *testBridge, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}, 0, "", 412, false, "not connected for this brain"},
		{"revoked", nil, 400, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`, 412, true, "Token has been expired or revoked.): it was revoked or has expired"},
		{"Google is down", nil, 503, `{"error":"backend_error"}`, 503, false, "usually temporary"},
		{"no refresh token", func(t *testing.T, _ *testBridge, path string) {
			tok := expired
			tok.RefreshToken = ""
			writeToken(t, path, tok)
		}, 0, "", 412, true, "the brain's Google sign-in cannot be used (the token has no refresh token — connect again)"},
		{"another OAuth client", func(t *testing.T, _ *testBridge, path string) {
			tok := expired
			tok.ClientID = "456-other.apps.googleusercontent.com"
			writeToken(t, path, tok)
		}, 0, "", 412, true, "signed in with another Google OAuth client"},
		{"no OAuth client in Settings", func(t *testing.T, tb *testBridge, _ string) {
			if err := os.Remove(tb.google.clientPath()); err != nil {
				t.Fatal(err)
			}
		}, 0, "", 412, true, "add your Google OAuth client first"},
		{"a token file that is not JSON", func(t *testing.T, _ *testBridge, path string) {
			if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, 0, "", 412, true, "unreadable token file"},
		{"a symlink in place of the token", func(t *testing.T, _ *testBridge, path string) {
			if err := os.Rename(path, path+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path+".real", path); err != nil {
				t.Fatal(err)
			}
		}, 0, "", 412, true, "it is a symlink"},
		{"a token file with a second link", func(t *testing.T, _ *testBridge, path string) {
			if err := os.Link(path, path+".copy"); err != nil {
				t.Fatal(err)
			}
		}, 0, "", 412, true, "more than one link"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCalendarAPI(t)
			tb, url, token := calendarBrain(t, f, []string{"you@example.com"}, "you@example.com", true)
			path := tb.google.tokenPath("a")
			writeToken(t, path, expired)
			if tc.setup != nil {
				tc.setup(t, tb, path)
			}
			if tc.token != 0 {
				google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(tc.token)
					fmt.Fprint(w, tc.body)
				}))
				defer google.Close()
				googleTokenEndpoint = google.URL + "/token"
			}
			code, doc := brainTool(t, calendarCmd, url, token, "list")
			msg, _ := doc["error"].(string)
			if code != 1 || !strings.Contains(msg, tc.want) || strings.Contains(msg, calendarReconnectHint) != tc.reconnect {
				t.Errorf("exit %d, %v; want an error with %q, reconnect hint %v", code, doc, tc.want, tc.reconnect)
			}
			w := tb.do("GET", "/brain/calendar/calendars", nil, func(r *http.Request) {
				r.Header.Del("X-Bridge-Token")
				r.Header.Set("Authorization", "Bearer "+token)
			})
			if w.Code != tc.status {
				t.Errorf("the brain API answered %d %s; want %d", w.Code, w.Body.String(), tc.status)
			}
		})
	}
}

// TestCalendarHintsNameTheDashboardsButtons: what a brain tells its owner
// to click is a button the brain's Calendar card shows: "Connect Google
// Calendar" until it is connected, "Sign in again" once Google refuses it.
func TestCalendarHintsNameTheDashboardsButtons(t *testing.T) {
	t.Parallel()
	js, err := os.ReadFile(filepath.Join(repoDir, "web", "assets", "components", "calendar.js"))
	if err != nil {
		t.Fatal(err)
	}
	_, notConnected := calendarCallProblem(fs.ErrNotExist)
	for _, label := range []string{"Sign in again", "Connect Google Calendar"} {
		if !strings.Contains(calendarReconnectHint+notConnected, label) {
			t.Errorf("no hint names %q", label)
		}
		if !strings.Contains(string(js), "'"+label+"'") {
			t.Errorf("the dashboard has no %q button", label)
		}
	}
}
