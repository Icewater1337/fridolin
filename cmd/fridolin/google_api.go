package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// isOAuthCallback reports whether a request to / is Google returning.
func isOAuthCallback(r *http.Request) bool {
	q := r.URL.Query()
	return q.Get("state") != "" && (q.Get("code") != "" || q.Get("error") != "")
}

func (b *Bridge) googleCallback(w http.ResponseWriter, r *http.Request) {
	redirect, err := b.google.Callback(r.Context(), r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, redirect, http.StatusFound)
}

// ── API ──────────────────────────────────────────────────────────────────

func (b *Bridge) apiGoogleClient(w http.ResponseWriter, r *http.Request) {
	g := b.google
	resp := map[string]any{"configured": false, "redirect_uri": g.RedirectURI()}
	if c := g.Client(); c != nil {
		hint := c.ClientID
		if i := strings.Index(hint, "-"); i > 0 {
			hint = hint[:i] + "-…"
		}
		resp["configured"], resp["type"], resp["client_id_hint"] = true, c.Type, hint
	}
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

func (b *Bridge) apiSetGoogleClient(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JSON string `json:"json"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := b.google.SetClient([]byte(strings.TrimSpace(req.JSON)))
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	note := ""
	if c.Type == "web" {
		note = "This is a Web application client: add " + b.google.RedirectURI() + " to its Authorized redirect URIs in Google Cloud Console, or create a Desktop app client instead."
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "saved", "type": c.Type, "note": note})
}

func (b *Bridge) apiDeleteGoogleClient(w http.ResponseWriter, r *http.Request) {
	if err := b.google.DeleteClient(); err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (b *Bridge) calendarBrain(w http.ResponseWriter, r *http.Request) *Brain {
	br := b.brainMgr.Get(r.PathValue("id"))
	if br == nil {
		httpapi.WriteErr(w, http.StatusNotFound, "no such brain")
	}
	return br
}

func (b *Bridge) apiCalendarConnect(w http.ResponseWriter, r *http.Request) {
	br := b.calendarBrain(w, r)
	if br == nil {
		return
	}
	u, err := b.google.StartSignIn(br.ID())
	if err != nil {
		httpapi.WriteErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"auth_url": u})
}

func (b *Bridge) apiCalendarStatus(w http.ResponseWriter, r *http.Request) {
	br := b.calendarBrain(w, r)
	if br == nil {
		return
	}
	m := br.settings.Load().manifest
	resp := b.google.CalendarStatus(r.Context(), br.ID(), m.Calendar, r.URL.Query().Get("list") != "0")
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

func (b *Bridge) apiCalendarSelect(w http.ResponseWriter, r *http.Request) {
	br := b.calendarBrain(w, r)
	if br == nil {
		return
	}
	var req struct {
		Account   string   `json:"account"`
		Calendars []string `json:"calendars"`
		Default   string   `json:"default"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	err := b.google.Select(br.ID(), manifest.Calendar{Account: req.Account, Calendars: req.Calendars, Default: req.Default})
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// apiCalendarDisconnect ends a brain's calendar access. ?local_only=1
// deletes the token without revoking it, for when Google would not confirm
// the revocation.
func (b *Bridge) apiCalendarDisconnect(w http.ResponseWriter, r *http.Request) {
	br := b.calendarBrain(w, r)
	if br == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	revoked, note, err := b.google.Disconnect(ctx, br.ID(), r.URL.Query().Get("local_only") == "1")
	switch {
	case errors.Is(err, ErrGoogleNotRevoked):
		httpapi.WriteErr(w, http.StatusBadGateway, err.Error()+"; the token was kept, so try again, or remove it here only")
		return
	case err != nil:
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"status": "disconnected", "revoked": revoked, "note": note})
}
