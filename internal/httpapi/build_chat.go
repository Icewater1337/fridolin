package httpapi

import (
	"net/http"
	"strings"

	"github.com/Icewater1337/fridolin/internal/brain"
)

type BuildChatAPI struct {
	Brain       func(string) *brain.Brain
	View        func(*brain.Brain) map[string]any
	AppendUser  func(string, string) (int, error)
	Submit      func(*brain.Brain, int, string) error
	NewThread   func(string) error
	StopCurrent func(*brain.Brain) bool
}

// buildBrain returns the builder brain a build request names.
func (a *BuildChatAPI) buildBrain(w http.ResponseWriter, r *http.Request) *brain.Brain {
	id := r.PathValue("id")
	br := a.Brain(id)
	if br == nil {
		WriteErr(w, http.StatusNotFound, "no builder brain "+id+" (Site → Builders)")
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
	WriteJSON(w, http.StatusOK, resp)
}

func (a *BuildChatAPI) Send(w http.ResponseWriter, r *http.Request) {
	br := a.buildBrain(w, r)
	if br == nil {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || len(text) > 20000 {
		WriteErr(w, http.StatusBadRequest, "write a message (up to 20,000 characters)")
		return
	}
	thread, err := a.AppendUser(br.ID(), text)
	if err != nil {
		WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.Submit(br, thread, text); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (a *BuildChatAPI) New(w http.ResponseWriter, r *http.Request) {
	br := a.buildBrain(w, r)
	if br == nil {
		return
	}
	if err := a.NewThread(br.ID()); err != nil {
		WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *BuildChatAPI) Stop(w http.ResponseWriter, r *http.Request) {
	br := a.buildBrain(w, r)
	if br == nil {
		return
	}
	if !a.StopCurrent(br) {
		WriteErr(w, http.StatusConflict, "nothing is running in this chat")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
