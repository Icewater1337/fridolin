package httpapi

import (
	"fmt"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/chat"
	"net/http"
	"strings"
	"time"
)

type DevMessage struct {
	TS   time.Time `json:"ts"`
	JID  string    `json:"jid"`
	Text string    `json:"text"`
	Poll []string  `json:"poll,omitempty"`
	// ID is a poll's message id, which a vote names.
	ID string `json:"id,omitempty"`
}
type DevAPI struct {
	HasBrain func(string) bool
	Submit   func(DevInboundRequest) string
	Messages func() []DevMessage
	// Vote records a vote in a poll the dev chat shows, as one from
	// WhatsApp is recorded.
	Vote func(DevVoteRequest) error
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
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.JID == "" {
		WriteErr(w, http.StatusBadRequest, "jid required")
		return
	}
	req.Brain = strings.ToLower(strings.TrimSpace(req.Brain))
	if req.Brain != "" && !a.HasBrain(req.Brain) {
		WriteErr(w, http.StatusBadRequest, fmt.Sprintf("unknown brain %q", req.Brain))
		return
	}
	if req.Sender == "" {
		req.Sender = chat.NormalizeJID(req.JID)
	}
	if reason := a.Submit(req); reason != "" {
		WriteJSON(w, http.StatusAccepted, DevIgnoredResponse{Status: "ignored", Reason: reason})
		return
	}
	WriteJSON(w, http.StatusAccepted, OperationResponse{Status: "accepted"})
}

// VoteHandler is POST /api/dev/vote.
func (a *DevAPI) VoteHandler(w http.ResponseWriter, r *http.Request) {
	var req DevVoteRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Poll == "" || req.Voter == "" {
		WriteErr(w, http.StatusBadRequest, "poll and voter required")
		return
	}
	if err := a.Vote(req); err != nil {
		WriteErr(w, statusFor(apperr.KindOf(err)), err.Error())
		return
	}
	WriteJSON(w, http.StatusAccepted, OperationResponse{Status: "accepted"})
}

func (a *DevAPI) Outbox(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, a.Messages())
}
