package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"net/http"
	"regexp"
	"strings"
)

type IngestPolicy struct {
	Enabled   bool
	OwnerChat string
}
type ScriptAPI struct {
	Authorize     func(*http.Request) (chat.Principal, error)
	Recipient     func(context.Context, string) (string, error)
	MaySend       func(chat.Principal, string) error
	TokenRequired error
	SendExternal  func(context.Context, chat.Principal, string, string) (string, error)
	IngestPolicy  func() IngestPolicy
	Brain         func(string) *brain.Brain
	IngestTarget  func(chat.Principal, *brain.Brain, []string, string, string, string) (string, error)
	// TakeCode hands over, once, the caught code that answers the request
	// id; ok is false when none is waiting for it.
	TakeCode func(request string) (code OTPCodeResponse, ok bool, err error)
}

// SendRequest is the JSON body expected by POST /send.
type SendRequest struct {
	JID  string `json:"jid"`
	Text string `json:"text"`
}

// IngestRequest is the JSON body expected by POST /ingest. Brain names the
// brain; Paths are files already written at the top of the media directory
// (or in the brain's own inbox), which move into the brain's inbox; Text is
// the message that goes with them.
type IngestRequest struct {
	Brain  string   `json:"brain"`
	Paths  []string `json:"paths"`
	Text   string   `json:"text"`
	Sender string   `json:"sender"`
	// Scope is the name Brain had before. It is no longer read: a request
	// that still sends it is refused with the field to use instead, rather
	// than failing on a brain it does not name.
	Scope string `json:"scope"`
}

// httpSend handles POST /send {"jid":"…","text":"…"} for local scripts.
// docs/API.md is its contract.
//
// The sent message is appended to the owning brain's history so the brain can
// see what was said on its behalf — for example, so the OTP guard recognises a
// code request that a script sent out.
func (a *ScriptAPI) Send(w http.ResponseWriter, r *http.Request) {
	who, err := a.Authorize(r)
	if err != nil {
		WriteErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req SendRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		WriteErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	// Blank text would send nothing (ChunkMessage gives no chunks for it),
	// so refuse it rather than answer "sent" and log a message to history.
	if req.JID == "" || strings.TrimSpace(req.Text) == "" {
		WriteErr(w, http.StatusBadRequest, "jid and text are required")
		return
	}
	// Refused before sending: a script gets a 400 for its own mistake rather
	// than a failed send, and dev mode, whose messenger takes any JID,
	// refuses it too. A bare number is the DM with it.
	jid, err := a.Recipient(r.Context(), req.JID)
	if err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.MaySend(who, jid); err != nil {
		status := http.StatusForbidden
		if errors.Is(err, a.TokenRequired) {
			status = http.StatusUnauthorized
		}
		WriteErr(w, status, err.Error())
		return
	}

	request, err := a.SendExternal(r.Context(), who, jid, req.Text)
	if err != nil {
		WriteErr(w, sendErrStatus(err), "send failed: "+err.Error())
		return
	}
	resp := SendResponse{Status: "sent"}
	if request != "" {
		resp.OTPRequest = request
	}
	WriteJSON(w, http.StatusOK, resp)
}

// sendErrStatus is the status for a send that failed with err: 503 while
// WhatsApp is not connected (try again later), 400 for a chat WhatsApp
// does not know, else 500.
func sendErrStatus(err error) int {
	switch {
	case errors.Is(err, outboundmsg.ErrOffline):
		return http.StatusServiceUnavailable
	case errors.Is(err, outboundmsg.ErrBadRecipient), errors.Is(err, outboundmsg.ErrRefusedRecipient):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// OTPTakeRequest is the JSON body expected by POST /otp/take.
type OTPTakeRequest struct {
	Request string `json:"request"`
}

// requestID is what NewRequestID in internal/otp makes: 16 hex digits.
var requestID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// TakeOTP handles POST /otp/take {"request":"…"}: it hands the code the OTP
// guard caught for that request id (the otp_request /send answered) to the
// script that asked, once, and deletes it. docs/API.md is its contract.
//
// Only the scripts token takes a code: brains never see one, which is why
// the sandbox denies them the hand-off folder too, and a program without a
// token gets none whatever script_api.tokenless_send says. A request with
// no code waiting, whether unknown, not answered yet, expired or taken
// already, is the same 404, which tells nothing about other requests.
func (a *ScriptAPI) TakeOTP(w http.ResponseWriter, r *http.Request) {
	who, err := a.Authorize(r)
	if err == nil && who.Kind == chat.CallerAnonymous {
		err = a.TokenRequired
	}
	if err != nil {
		WriteErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if who.Kind != chat.CallerScript {
		WriteErr(w, http.StatusForbidden, "one-time codes go only to scripts with the scripts token, never to a brain")
		return
	}
	var req OTPTakeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		WriteErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if !requestID.MatchString(req.Request) {
		WriteErr(w, http.StatusBadRequest, `"request" must be the otp_request id /send answered with`)
		return
	}
	code, ok, err := a.TakeCode(req.Request)
	if err != nil {
		WriteErr(w, http.StatusInternalServerError, "taking the code failed")
		return
	}
	if !ok {
		WriteErr(w, http.StatusNotFound, "no code for this request")
		return
	}
	WriteJSON(w, http.StatusOK, code)
}

// httpIngest handles POST /ingest. It feeds files that arrived outside
// WhatsApp (a web upload, a recorder sync) through the same pipeline as a
// WhatsApp attachment. They run in the brain the request names, and the
// brain's reply goes to its reply chat, even when that chat routes to another
// brain. Responds 202 at once; processing is asynchronous. docs/API.md is
// its contract.
//
// It needs a token: the brain it names runs with its own folder, memories
// and credentials, so handing work to a brain is not for any program on
// this computer. A brain may hand files only to itself.
func (a *ScriptAPI) Ingest(w http.ResponseWriter, r *http.Request) {
	policy := a.IngestPolicy()
	if !policy.Enabled {
		WriteErr(w, http.StatusNotFound, "ingest is disabled (set ingest.enabled in the dashboard)")
		return
	}
	who, err := a.Authorize(r)
	if err == nil && who.Kind == chat.CallerAnonymous {
		err = a.TokenRequired
	}
	if err != nil {
		WriteErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req IngestRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		WriteErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Scope != "" {
		WriteErr(w, http.StatusBadRequest, `"scope" is no longer read: name the brain in "brain"`)
		return
	}
	if len(req.Paths) == 0 && strings.TrimSpace(req.Text) == "" {
		WriteErr(w, http.StatusBadRequest, "paths or text required")
		return
	}
	id := strings.ToLower(strings.TrimSpace(req.Brain))
	if who.Kind == chat.CallerBrain {
		if id == "" {
			id = who.Brain
		}
		if id != who.Brain {
			WriteErr(w, http.StatusForbidden, fmt.Sprintf("brain %s may hand files only to itself, not to %q", who.Brain, id))
			return
		}
	}
	br := a.Brain(id)
	if br == nil {
		WriteErr(w, http.StatusBadRequest, fmt.Sprintf("unknown brain %q", id))
		return
	}

	jid, err := a.IngestTarget(who, br, req.Paths, req.Sender, req.Text, policy.OwnerChat)
	if err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusAccepted, IngestAcceptedResponse{Status: "accepted", JID: jid})
}
