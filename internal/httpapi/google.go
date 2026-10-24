package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/backup"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/google"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// GoogleService is the calendar and client surface used by HTTP handlers.
type GoogleService interface {
	Callback(context.Context, url.Values) (string, error)
	RedirectURI() string
	Client() *google.Client
	Builtin() *google.Client
	CheckClient(context.Context) (google.ClientCheck, error)
	SetClient([]byte) (*google.Client, error)
	DeleteClient() error
	StartSignIn(string) (string, error)
	CalendarStatus(context.Context, string, *manifest.Calendar, bool) google.CalendarSnapshot
	Select(string, manifest.Calendar) error
	Disconnect(context.Context, string, bool) (bool, string, error)
	Health(string) *google.GrantHealth
}

type GoogleAPI struct {
	Google func() GoogleService
	Brain  func(string) *brain.Brain
	// Brains are the brains the bridge runs, for CalendarHealth.
	Brains func() []*brain.Brain
}

// CalendarHealth answers GET /api/calendar/health: the calendar grant of
// each brain that is connected, for the Overview's warning.
func (a *GoogleAPI) CalendarHealth(w http.ResponseWriter, r *http.Request) {
	resp := CalendarHealthResponse{Brains: []CalendarHealthRow{}}
	if a.Brains != nil {
		for _, br := range a.Brains() {
			if h := a.Google().Health(br.ID()); h != nil {
				resp.Brains = append(resp.Brains, CalendarHealthRow{ID: br.ID(), Name: br.Name(), GrantHealth: *h})
			}
		}
	}
	WriteJSON(w, http.StatusOK, resp)
}

// IsOAuthCallback reports whether a request to / is Google returning.
func IsOAuthCallback(r *http.Request) bool {
	q := r.URL.Query()
	return q.Get("state") != "" && (q.Get("code") != "" || q.Get("error") != "")
}

func (a *GoogleAPI) Callback(w http.ResponseWriter, r *http.Request) {
	redirect, err := a.Google().Callback(r.Context(), r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, redirect, http.StatusFound)
}

// ── API ──────────────────────────────────────────────────────────────────

func (a *GoogleAPI) Client(w http.ResponseWriter, r *http.Request) {
	g := a.Google()
	resp := GoogleClientResponse{Configured: false, RedirectURI: g.RedirectURI(), Builtin: g.Builtin() != nil,
		Scopes: append(google.CalendarScopes(), backup.DriveScope)}
	if c := g.Client(); c != nil {
		hint := c.ClientID
		if i := strings.Index(hint, "-"); i > 0 {
			hint = hint[:i] + "-…"
		}
		clientType, source := c.Type, c.Source
		resp.Configured, resp.Type, resp.ClientIDHint, resp.Source = true, &clientType, &hint, &source
	}
	WriteJSON(w, http.StatusOK, resp)
}

// CheckClient checks the client sign-ins use, with a request to Google
// that grants nothing (google.CheckClient).
func (a *GoogleAPI) CheckClient(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := a.Google().CheckClient(ctx)
	if errors.Is(err, google.ErrNoClient) {
		WriteErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	if err != nil {
		WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, res)
}

// PastedCallback finishes a sign-in whose return address did not load: a
// dashboard opened from another machine (the core over the tailnet, say)
// sends the browser back to the bridge's loopback address on that
// machine, where nothing answers. The owner pastes the address from the
// browser's address bar instead; its one-time state ties it to a sign-in
// the dashboard started, as at the redirect itself.
func (a *GoogleAPI) PastedCallback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := url.Parse(strings.TrimSpace(req.URL))
	q := url.Values{}
	if err == nil {
		q = u.Query()
	}
	if err != nil || q.Get("state") == "" || (q.Get("code") == "" && q.Get("error") == "") {
		WriteErr(w, http.StatusUnprocessableEntity, "that is not the address Google sent the browser to: copy the whole address of the page that did not load, starting with http://127.0.0.1")
		return
	}
	redirect, err := a.Google().Callback(r.Context(), q)
	if err != nil {
		WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, GoogleCallbackResponse{Redirect: redirect})
}

func (a *GoogleAPI) SetClient(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JSON string `json:"json"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := a.Google().SetClient([]byte(strings.TrimSpace(req.JSON)))
	if err != nil {
		WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	note := ""
	if c.Type == "web" {
		note = "This is a Web application client: add " + a.Google().RedirectURI() + " to its Authorized redirect URIs in Google Cloud Console, or create a Desktop app client instead."
	}
	WriteJSON(w, http.StatusOK, GoogleClientSavedResponse{Status: "saved", Type: c.Type, Note: note})
}

func (a *GoogleAPI) DeleteClient(w http.ResponseWriter, r *http.Request) {
	if err := a.Google().DeleteClient(); err != nil {
		WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "removed"})
}

func (a *GoogleAPI) calendarBrain(w http.ResponseWriter, r *http.Request) *brain.Brain {
	br := a.Brain(r.PathValue("id"))
	if br == nil {
		WriteErr(w, http.StatusNotFound, "no such brain")
	}
	return br
}

func (a *GoogleAPI) Connect(w http.ResponseWriter, r *http.Request) {
	br := a.calendarBrain(w, r)
	if br == nil {
		return
	}
	u, err := a.Google().StartSignIn(br.ID())
	if err != nil {
		WriteErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, AuthURLResponse{AuthURL: u})
}

func (a *GoogleAPI) Status(w http.ResponseWriter, r *http.Request) {
	br := a.calendarBrain(w, r)
	if br == nil {
		return
	}
	m := br.Manifest()
	resp := a.Google().CalendarStatus(r.Context(), br.ID(), m.Calendar, r.URL.Query().Get("list") != "0")
	WriteJSON(w, http.StatusOK, resp)
}

func (a *GoogleAPI) Select(w http.ResponseWriter, r *http.Request) {
	br := a.calendarBrain(w, r)
	if br == nil {
		return
	}
	var req struct {
		Account   string   `json:"account"`
		Calendars []string `json:"calendars"`
		Default   string   `json:"default"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	err := a.Google().Select(br.ID(), manifest.Calendar{Account: req.Account, Calendars: req.Calendars, Default: req.Default})
	if err != nil {
		WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "saved"})
}

// apiCalendarDisconnect ends a brain's calendar access. ?local_only=1
// deletes the token without revoking it, for when Google would not confirm
// the revocation.
func (a *GoogleAPI) Disconnect(w http.ResponseWriter, r *http.Request) {
	br := a.calendarBrain(w, r)
	if br == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	revoked, note, err := a.Google().Disconnect(ctx, br.ID(), r.URL.Query().Get("local_only") == "1")
	switch {
	case errors.Is(err, google.ErrNotRevoked):
		WriteErr(w, http.StatusBadGateway, err.Error()+"; the token was kept, so try again, or remove it here only")
		return
	case err != nil:
		WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, CalendarDisconnectedResponse{Status: "disconnected", Revoked: revoked, Note: note})
}
