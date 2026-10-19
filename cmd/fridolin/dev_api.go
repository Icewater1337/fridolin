package main

import (
	"fmt"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"net/http"
	"strings"
	"time"
)

type DevMessage struct {
	TS   time.Time `json:"ts"`
	JID  string    `json:"jid"`
	Text string    `json:"text"`
	Poll []string  `json:"poll,omitempty"`
}
type DevAPI struct {
	HasBrain func(string) bool
	Submit   func(DevInboundRequest) string
	Messages func() []DevMessage
}
type DevInboundRequest struct {
	JID    string   `json:"jid"`
	Sender string   `json:"sender"`
	Text   string   `json:"text"`
	Media  []string `json:"media"`
	Silent bool     `json:"silent"`
	// Brain runs the message in that brain instead of the chat's own,
	// the way /ingest and Plaud hand files to a brain.
	Brain string `json:"brain"`
}

func (a *DevAPI) Inbound(w http.ResponseWriter, r *http.Request) {
	var req DevInboundRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.JID == "" {
		httpapi.WriteErr(w, http.StatusBadRequest, "jid required")
		return
	}
	req.Brain = strings.ToLower(strings.TrimSpace(req.Brain))
	if req.Brain != "" && !a.HasBrain(req.Brain) {
		httpapi.WriteErr(w, http.StatusBadRequest, fmt.Sprintf("unknown brain %q", req.Brain))
		return
	}
	if req.Sender == "" {
		req.Sender = chat.NormalizeJID(req.JID)
	}
	if reason := a.Submit(req); reason != "" {
		httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "ignored", "reason": reason})
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (a *DevAPI) Outbox(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, a.Messages())
}
