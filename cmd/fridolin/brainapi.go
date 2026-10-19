package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/google"
	"github.com/Icewater1337/fridolin/internal/httpapi"

	"github.com/Icewater1337/fridolin/internal/chat"
)

// The brain API, /brain/, is what the tools a brain runs ask the bridge
// for: poll results and calendar calls (google_calendar.go). The bridge
// answers them with what a brain's sandbox keeps from it (bridge.db, the
// Google tokens) and decides itself what the brain may see, so a brain
// cannot widen that by changing its environment or writing its own client.
//
// Each run gets a token of its own, bound to the brain, and the token
// stops working when the run's CLI has exited. It opens /brain/, and
// /send and /ingest for the brain's own chats and itself (scripttoken.go),
// and nothing else: the dashboard API needs the dashboard token, which
// brains cannot read. The token is in a file that only its brain's sandbox
// profile can read, and the run's environment names the file
// (FRIDOLIN_BRAIN_TOKEN_FILE). The token itself stays out of the
// environment: macOS shows a process's environment to every process of
// the same user (sysctl KERN_PROCARGS2, as `ps eww` reads it), sandboxed
// or not, so a brain could read the token of another brain's run in
// progress and call the bridge as that brain.

// runTokens are the brain API tokens of the runs in progress.
type runTokens struct {
	mu sync.Mutex
	// brains maps the SHA-256 of a token to its brain. The lookup goes by
	// hash, so its timing says nothing about a token's characters.
	brains map[[sha256.Size]byte]string
}

// mint makes a token for one run of brain, and the function that revokes
// it once the run is over.
func (rt *runTokens) mint(brain string) (string, func()) {
	tok := randomToken(24)
	sum := sha256.Sum256([]byte(tok))
	rt.mu.Lock()
	if rt.brains == nil {
		rt.brains = map[[sha256.Size]byte]string{}
	}
	rt.brains[sum] = brain
	rt.mu.Unlock()
	var once sync.Once
	return tok, func() {
		once.Do(func() {
			rt.mu.Lock()
			delete(rt.brains, sum)
			rt.mu.Unlock()
		})
	}
}

// brainOf returns the brain a token was minted for, or "".
func (rt *runTokens) brainOf(tok string) string {
	if tok == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(tok))
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.brains[sum]
}

// brainRunEnv is what a run of brain gets to use the bridge's commands: the
// binary, for "$FRIDOLIN_BIN" calendar and the like, and the brain API's
// address with the file that holds a token for this run. Prompts and
// skills name the binary by the variable, never by a path, so a brain
// folder keeps working when it or the bridge moves; FRIDOLIN_TOOLS_DIR
// does the same for the Python helpers (see invoke). The second result
// revokes the token and deletes its file.
func (b *Bridge) brainRunEnv(brain string) ([]string, func()) {
	var env []string
	if b.exe != "" {
		env = append(env, "FRIDOLIN_BIN="+b.exe)
	}
	if b.listen == "" {
		return env, func() {}
	}
	tok, revoke := b.runTokens.mint(brain)
	path, err := writeRunToken(runTokenDir(b.cfgs.Boot().DataDir, brain), tok)
	if err != nil {
		revoke()
		log.Printf("brain %s: no brain API for this run: %v", brain, err)
		return env, func() {}
	}
	return append(env, "FRIDOLIN_API=http://"+b.listen, "FRIDOLIN_BRAIN_TOKEN_FILE="+path), func() {
		revoke()
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("brain %s: removing the run's brain API token: %v", brain, err)
		}
	}
}

// runTokenDir is the folder of brain's brain API tokens, one file per run
// in progress. It is in the data dir, which the sandbox denies every
// brain; brain's own profile opens this folder again, for reading only.
// Absolute, since the run's tools resolve it in the brain's folder.
func runTokenDir(dataDir, brain string) string {
	return filepath.Join(absPath(dataDir), runTokensDirName, brain)
}

// runTokensDirName is the data dir's folder of run tokens.
const runTokensDirName = "run-tokens"

// writeRunToken writes tok to a new file in dir, readable by the bridge's
// user only, and returns its path. Each run gets a file of its own: one
// brain's runs can overlap while an interrupted one winds down.
func writeRunToken(dir, tok string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, "run-*.token")
	if err != nil {
		return "", fmt.Errorf("creating the token file: %w", err)
	}
	_, err = f.WriteString(tok)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("writing the token file: %w", err)
	}
	return f.Name(), nil
}

// pruneRunTokens deletes the token files a bridge that stopped without
// cleaning up left in dataDir. Their tokens died with it; only the files
// are left.
func pruneRunTokens(dataDir string) {
	files, _ := filepath.Glob(filepath.Join(absPath(dataDir), runTokensDirName, "*", "run-*.token"))
	for _, f := range files {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("warning: removing a stale brain API token: %v", err)
		}
	}
}

// bridgeBinary is the running binary, which brains run as $FRIDOLIN_BIN:
// the copy `fridolin service install` made, or a build. Without it, or
// without the Python helpers next to it, the log says what brains lack.
func bridgeBinary() string {
	if ownLayout().Tools == "" {
		log.Printf("warning: the bridge has no tools/ next to its binary: brains get no FRIDOLIN_TOOLS_DIR, so tools/transcribe.py is not there for them")
	}
	exe, err := os.Executable()
	if err != nil {
		log.Printf("warning: cannot tell where the bridge's binary is (%v): brains get no FRIDOLIN_BIN, so fridolin calendar, polls and history fail in them", err)
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe
}

// brainAPIHandler serves /brain/.
func (b *Bridge) brainAPIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /brain/polls", b.brainCall(b.brainPolls))
	mux.HandleFunc("GET /brain/calendar/calendars", b.brainCall(b.calendarCall(b.brainCalendarList)))
	mux.HandleFunc("GET /brain/calendar/events", b.brainCall(b.calendarCall(b.brainCalendarEvents)))
	mux.HandleFunc("POST /brain/calendar/events", b.brainCall(b.calendarCall(b.brainCalendarCreate)))
	mux.HandleFunc("DELETE /brain/calendar/events/{id}", b.brainCall(b.calendarCall(b.brainCalendarDelete)))
	mux.HandleFunc("POST /brain/calendar/freebusy", b.brainCall(b.calendarCall(b.brainCalendarBusy)))
	mux.HandleFunc("/brain/", func(w http.ResponseWriter, r *http.Request) {
		httpapi.WriteErr(w, http.StatusNotFound, "no such brain API call: "+r.Method+" "+r.URL.Path)
	})
	return mux
}

// brainCall authenticates a brain API call by the run's token and hands
// the brain it belongs to on to fn.
func (b *Bridge) brainCall(fn func(w http.ResponseWriter, r *http.Request, br *Brain)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		br := b.brainMgr.Get(b.runTokens.brainOf(tok))
		if br == nil {
			httpapi.WriteErr(w, http.StatusUnauthorized, "the bridge does not know this run's token (FRIDOLIN_BRAIN_TOKEN_FILE): it works only inside the run it was made for")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		}
		fn(w, r, br)
	}
}

// ownsChat reports whether chat is one of br's: a chat that routes to it,
// or the one it replies in. A brain sees only the polls of those, since a
// poll's voters are the people of the chat it was sent to.
func (b *Bridge) ownsChat(br *Brain, chatJID string) bool {
	return b.brainMgr.BrainFor(chatJID, chat.IsGroupJID(chatJID)) == br || b.replyChatFor(br, b.cfgs.Get()) == chatJID
}

// brainPolls answers GET /brain/polls: ?id=MSG_ID for one poll, else the
// ?recent=N (default 10) newest, of ?chat=JID when given.
func (b *Bridge) brainPolls(w http.ResponseWriter, r *http.Request, br *Brain) {
	q := r.URL.Query()
	owns := func(chatJID string) bool { return b.ownsChat(br, chatJID) }
	if id := q.Get("id"); id != "" {
		p, err := b.store.Poll(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !owns(p.ChatJID)) {
			httpapi.WriteErr(w, http.StatusNotFound, fmt.Sprintf("poll %q not found in this brain's chats", id))
			return
		}
		if err != nil {
			httpapi.WriteErr(w, http.StatusInternalServerError, "reading the poll: "+err.Error())
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, p)
		return
	}
	recent := 10
	if s := q.Get("recent"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			httpapi.WriteErr(w, http.StatusBadRequest, "recent must be a number from 1 to 100")
			return
		}
		recent = n
	}
	chatJID := q.Get("chat")
	if chatJID != "" && !owns(chatJID) {
		httpapi.WriteErr(w, http.StatusForbidden, fmt.Sprintf("chat %q is not one of this brain's chats", chatJID))
		return
	}
	polls, err := b.store.RecentPolls(r.Context(), chatJID, recent, owns)
	if err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, "reading the polls: "+err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"polls": polls, "count": len(polls)})
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
func (b *Bridge) calendarCall(fn calendarHandler) func(w http.ResponseWriter, r *http.Request, br *Brain) {
	return func(w http.ResponseWriter, r *http.Request, br *Brain) {
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		cals := google.CalendarsOf(br.settings.Load().manifest)
		out, err := fn(ctx, r, cals, func() (*google.Token, error) { return b.google.AccessToken(ctx, br.ID()) })
		if err != nil {
			var ce *callError
			if errors.As(err, &ce) {
				httpapi.WriteErr(w, ce.status, ce.msg)
				return
			}
			status, msg := google.CallProblem(err)
			httpapi.WriteErr(w, status, msg)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, out)
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
func (b *Bridge) brainCalendarList(ctx context.Context, r *http.Request, cals google.Calendars, token func() (*google.Token, error)) (any, error) {
	tok, err := token()
	if err != nil {
		return nil, err
	}
	list, err := b.google.CalendarList(ctx, tok)
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
	return map[string]any{"calendars": out, "restricted": true}, nil
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
func (b *Bridge) brainCalendarEvents(ctx context.Context, r *http.Request, cals google.Calendars, token func() (*google.Token, error)) (any, error) {
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
	return b.google.EventsOf(ctx, tok, calendars, from, to, q.Get("time_zone"), limit)
}

var sendUpdatesValues = []string{"all", "externalOnly", "none"}

// brainCalendarCreate answers POST /brain/calendar/events: a NewEvent,
// into its calendar, else the brain's default one.
func (b *Bridge) brainCalendarCreate(ctx context.Context, r *http.Request, cals google.Calendars, token func() (*google.Token, error)) (any, error) {
	var ev google.NewEvent
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ev); err != nil {
		return nil, badCall("the event is not valid JSON: %v", google.DecodeError(err))
	}
	body, err := ev.Body()
	if err != nil {
		return nil, badCall("%v", err)
	}
	if ev.SendUpdates == "" {
		ev.SendUpdates = "all"
	}
	if !slices.Contains(sendUpdatesValues, ev.SendUpdates) {
		return nil, badCall("send_updates must be one of %s", strings.Join(sendUpdatesValues, ", "))
	}
	calendars, err := cals.Pick(ev.Calendar, false)
	if err != nil {
		return nil, err
	}
	tok, err := token()
	if err != nil {
		return nil, err
	}
	created, err := b.google.InsertEvent(ctx, tok, calendars[0], body, ev.SendUpdates)
	if err != nil {
		return nil, err
	}
	return map[string]any{"created": created}, nil
}

// brainCalendarDelete answers DELETE /brain/calendar/events/{id}: in
// ?calendar=, else the brain's default calendar.
func (b *Bridge) brainCalendarDelete(ctx context.Context, r *http.Request, cals google.Calendars, token func() (*google.Token, error)) (any, error) {
	id := r.PathValue("id")
	calendars, err := cals.Pick(r.URL.Query().Get("calendar"), false)
	if err != nil {
		return nil, err
	}
	tok, err := token()
	if err != nil {
		return nil, err
	}
	if err := b.google.DeleteEvent(ctx, tok, calendars[0], id); err != nil {
		return nil, err
	}
	return map[string]string{"deleted": id}, nil
}

// brainCalendarBusy answers POST /brain/calendar/freebusy {from, to,
// time_zone, calendar}: when the calendar, else any of the brain's
// calendars, is busy.
func (b *Bridge) brainCalendarBusy(ctx context.Context, r *http.Request, cals google.Calendars, token func() (*google.Token, error)) (any, error) {
	var req struct {
		From     string `json:"from"`
		To       string `json:"to"`
		TimeZone string `json:"time_zone"`
		Calendar string `json:"calendar"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
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
	busy, err := b.google.BusyTimes(ctx, tok, calendars, from, to, req.TimeZone)
	if err != nil {
		return nil, err
	}
	return map[string]any{"busy": busy, "calendars": calendars}, nil
}
