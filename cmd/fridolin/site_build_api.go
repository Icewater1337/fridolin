package main

import (
	"net/http"
	"strings"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

type BuildChatAPI struct {
	Brain       func(string) *brain.Brain
	View        func(*brain.Brain) map[string]any
	AppendUser  func(string, string) (int, error)
	Submit      func(*brain.Brain, int, string) error
	NewThread   func(string) error
	StopCurrent func(*brain.Brain) bool
}

func (a *BuildChatAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/site/build/{id}", a.Chat)
	mux.HandleFunc("POST /api/site/build/{id}", a.Send)
	mux.HandleFunc("POST /api/site/build/{id}/new", a.New)
	mux.HandleFunc("POST /api/site/build/{id}/stop", a.Stop)
}

// buildBrain returns the builder brain a build request names.
func (a *BuildChatAPI) buildBrain(w http.ResponseWriter, r *http.Request) *brain.Brain {
	id := r.PathValue("id")
	br := a.Brain(id)
	if br == nil {
		httpapi.WriteErr(w, http.StatusNotFound, "no builder brain "+id+" (Site → Builders)")
		return nil
	}
	return br
}

func (a *BuildChatAPI) Chat(w http.ResponseWriter, r *http.Request) {
	br := a.buildBrain(w, r)
	if br == nil {
		return
	}
	resp := a.View(br)
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

func (a *BuildChatAPI) Send(w http.ResponseWriter, r *http.Request) {
	br := a.buildBrain(w, r)
	if br == nil {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || len(text) > 20000 {
		httpapi.WriteErr(w, http.StatusBadRequest, "write a message (up to 20,000 characters)")
		return
	}
	thread, err := a.AppendUser(br.ID(), text)
	if err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.Submit(br, thread, text); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (a *BuildChatAPI) New(w http.ResponseWriter, r *http.Request) {
	br := a.buildBrain(w, r)
	if br == nil {
		return
	}
	if err := a.NewThread(br.ID()); err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *BuildChatAPI) Stop(w http.ResponseWriter, r *http.Request) {
	br := a.buildBrain(w, r)
	if br == nil {
		return
	}
	if !a.StopCurrent(br) {
		httpapi.WriteErr(w, http.StatusConflict, "nothing is running in this chat")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
