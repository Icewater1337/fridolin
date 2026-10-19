package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/route"

	"github.com/Icewater1337/fridolin/internal/httpapi"

	"go.mau.fi/whatsmeow/types"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/otp"
	"github.com/Icewater1337/fridolin/internal/version"
)

// sendRequest is the JSON body expected by POST /send.
type sendRequest struct {
	JID  string `json:"jid"`
	Text string `json:"text"`
}

// ingestRequest is the JSON body expected by POST /ingest. Brain names the
// brain; Paths are files already written at the top of the media directory
// (or in the brain's own inbox), which move into the brain's inbox; Text is
// the message that goes with them.
type ingestRequest struct {
	Brain  string   `json:"brain"`
	Paths  []string `json:"paths"`
	Text   string   `json:"text"`
	Sender string   `json:"sender"`
	// Scope is the name Brain had before. It is no longer read: a request
	// that still sends it is refused with the field to use instead, rather
	// than failing on a brain it does not name.
	Scope string `json:"scope"`
}

// readyResponse is the body of GET /ready: whether messages can go out and
// which build answers. Nothing in it identifies the account.
type readyResponse struct {
	Ready     bool      `json:"ready"`
	WhatsApp  string    `json:"whatsapp"` // WAState.Mode
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
}

// httpReady handles GET /ready: 200 when /send can deliver (WhatsApp is
// connected, or dev mode simulates it), else 503 with the same body, so a
// script can tell a bridge that still needs pairing from one that was
// logged out. /health stays plain liveness: right after setup the bridge
// is up but not paired, and waiting for ready there would never end.
func (b *Bridge) httpReady(w http.ResponseWriter, r *http.Request) {
	mode := b.wa.State().Mode
	resp := readyResponse{
		Ready:     mode == "connected" || b.dev != nil,
		WhatsApp:  mode,
		Version:   version.String(),
		StartedAt: b.started,
	}
	status := http.StatusOK
	if !resp.Ready {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, status, resp)
}

// httpSend handles POST /send {"jid":"…","text":"…"} for local scripts.
// docs/API.md is its contract.
//
// The sent message is appended to the owning brain's history so the brain can
// see what was said on its behalf — for example, so the OTP guard recognises a
// code request that a script sent out.
func (b *Bridge) httpSend(w http.ResponseWriter, r *http.Request) {
	who, err := b.scriptAuth.authorize(r)
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req sendRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	// Blank text would send nothing (ChunkMessage gives no chunks for it),
	// so refuse it rather than answer "sent" and log a message to history.
	if req.JID == "" || strings.TrimSpace(req.Text) == "" {
		httpapi.WriteErr(w, http.StatusBadRequest, "jid and text are required")
		return
	}
	// Refused before sending: a script gets a 400 for its own mistake rather
	// than a failed send, and dev mode, whose messenger takes any JID,
	// refuses it too. A bare number is the DM with it.
	to, err := chatJID(req.JID)
	if err == nil {
		err = checkRecipient(to)
	}
	if err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// A person is their number to the bridge (canonicalUser): sent to a
	// LID, the message would go into a history the chat's runs, which
	// look under the number, find only when they know the LID too.
	if to.Server == types.HiddenUserServer && b.lids != nil {
		to = canonicalUser(r.Context(), b.lids(), to, types.EmptyJID)
	}
	jid := to.String()
	if err := b.maySend(who, jid, b.cfgs.Get()); err != nil {
		status := http.StatusForbidden
		if errors.Is(err, errTokenRequired) {
			status = http.StatusUnauthorized
		}
		httpapi.WriteErr(w, status, err.Error())
		return
	}

	// Under the chat's JID as inbound messages have it, whatever form the
	// request used, so the brain's next run sees it in the chat's history.
	if err := b.wa.SendChunked(r.Context(), jid, req.Text); err != nil {
		log.Printf("HTTP send error to %s: %v", jid, err)
		// What did go out is in the chat, so it goes in the history too.
		var partial *outboundmsg.PartialSendError
		if errors.As(err, &partial) {
			b.appendHTTPMessageToHistory(who, jid, strings.Join(partial.Sent, "\n"), "")
		}
		httpapi.WriteErr(w, sendErrStatus(err), "send failed: "+err.Error())
		return
	}

	// A message that asks for a one-time code gets an id, which the
	// hand-off of the code that answers it repeats (see handOffCode).
	resp := map[string]string{"status": "sent"}
	request := ""
	if br := b.brainMgr.BrainFor(jid, chat.IsGroupJID(jid)); br != nil && br.AsksForCode(req.Text) {
		request = otp.NewRequestID()
		resp["otp_request"] = request
	}
	b.appendHTTPMessageToHistory(who, jid, req.Text, request)
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

// maySend refuses a /send that who may not make: a brain may send only to
// its own chats (see brainChat), a caller without a token only to the
// allowed chats and only while script_api.tokenless_send is on. The
// scripts token may send anywhere checkSendJID lets through.
func (b *Bridge) maySend(who principal, jid string, cfg *config.Config) error {
	switch who.kind {
	case callerScript:
		return nil
	case callerBrain:
		br := b.brainMgr.Get(who.brain)
		if br == nil || !b.brainChat(br, jid, cfg) {
			return fmt.Errorf("brain %s may send only to its own chats, and %s is not one of them", who.brain, jid)
		}
		return nil
	}
	if !cfg.TokenlessSend() {
		return fmt.Errorf("%w (script_api.tokenless_send is off)", errTokenRequired)
	}
	if !allowedChat(jid, cfg) {
		return fmt.Errorf("without a token, /send reaches only the allowed chats (allowed_numbers, allowed_groups), and %s is not one", jid)
	}
	return nil
}

// brainChat reports whether jid is one of br's own chats: its reply chat
// (reply_chat, or the one replyChatFor derives), or an allowed chat that
// routes to it: a group it claims, for the fallback brain an allowed group
// no brain claims, or, for the brain that gets DMs, an allowed number's DM.
func (b *Bridge) brainChat(br *Brain, jid string, cfg *config.Config) bool {
	if jid == b.replyChatFor(br, cfg) {
		return true
	}
	return allowedChat(jid, cfg) && b.brainMgr.BrainFor(jid, chat.IsGroupJID(jid)) == br
}

// allowedChat reports whether jid is a chat config allows by name: a group
// in allowed_groups, or the chat of a number in allowed_numbers. allow_any_dm
// does not count: it lets anyone write in, not the bridge write to anyone.
func allowedChat(jid string, cfg *config.Config) bool {
	if chat.IsGroupJID(jid) {
		return slices.Contains(cfg.AllowedGroups, jid)
	}
	return strings.HasSuffix(jid, "@"+types.DefaultUserServer) && slices.Contains(cfg.AllowedNumbers, chat.NormalizeJID(jid))
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

// appendHTTPMessageToHistory records a message who sent through /send in the
// history of whichever brain owns the target chat, marked with how it came
// in: the brain must not take a line a script or another brain sent for its
// own words. request is the id of the code request it makes, "" for none.
func (b *Bridge) appendHTTPMessageToHistory(who principal, jid, text, request string) {
	br := b.brainMgr.BrainFor(jid, chat.IsGroupJID(jid))
	if br == nil {
		return
	}
	from := "script"
	if who.kind == callerBrain {
		from = who.brain
	}
	br.AppendEntry(history.Entry{From: from, Chat: jid, Text: text, Type: history.Out, Via: who.via(), OTPRequest: request})
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
func (b *Bridge) httpIngest(w http.ResponseWriter, r *http.Request) {
	cfg := b.cfgs.Get()
	if !cfg.Ingest.Enabled {
		httpapi.WriteErr(w, http.StatusNotFound, "ingest is disabled (set ingest.enabled in the dashboard)")
		return
	}
	who, err := b.scriptAuth.authorize(r)
	if err == nil && who.kind == callerAnonymous {
		err = errTokenRequired
	}
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req ingestRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Scope != "" {
		httpapi.WriteErr(w, http.StatusBadRequest, `"scope" is no longer read: name the brain in "brain"`)
		return
	}
	if len(req.Paths) == 0 && strings.TrimSpace(req.Text) == "" {
		httpapi.WriteErr(w, http.StatusBadRequest, "paths or text required")
		return
	}
	id := strings.ToLower(strings.TrimSpace(req.Brain))
	if who.kind == callerBrain {
		if id == "" {
			id = who.brain
		}
		if id != who.brain {
			httpapi.WriteErr(w, http.StatusForbidden, fmt.Sprintf("brain %s may hand files only to itself, not to %q", who.brain, id))
			return
		}
	}
	br := b.brainMgr.Get(id)
	if br == nil {
		httpapi.WriteErr(w, http.StatusBadRequest, fmt.Sprintf("unknown brain %q", id))
		return
	}

	paths, err := mediaInboxAt(b.cfgs.Boot().MediaDir).Resolve(br.ID(), req.Paths, who.kind == callerBrain)
	if err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}

	jid := b.replyChatFor(br, cfg)
	if jid == "" {
		httpapi.WriteErr(w, http.StatusBadRequest, "brain "+br.ID()+" has no chat to reply in — set reply_chat in its settings")
		return
	}
	sender := req.Sender
	if sender == "" {
		sender = "upload"
	}
	text := req.Text
	if text == "" {
		text = "New files were uploaded."
	}
	go b.handOff(br, jid, sender, text, paths)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "jid": jid})
}

// handOff feeds files that arrived outside WhatsApp to the brain picked for
// them, for /ingest and the Plaud integration. The message names the brain,
// so it runs there even when jid, the chat that gets the answer, belongs to
// another brain.
func (b *Bridge) handOff(br *Brain, jid, sender, text string, media []string) {
	m := Inbound{Inbound: chat.Inbound{JID: jid, Brain: br.ID(), Sender: sender, Text: text, Media: media, Source: chat.SourceIngest, At: time.Now()}}
	b.handleInbound(context.Background(), m)
}

// submitHandOff is handOff for a source that must know whether the brain
// took the files, as Plaud must: a recording cannot be sent again by
// whoever sent it. It skips the debouncer, which keeps messages only in
// memory, and queues the message at once: an error means the brain did
// not take it (its queue is full, or it is gone). done then hears how the
// run ended, see Inbound.Done.
func (b *Bridge) submitHandOff(br *Brain, jid, sender, text string, media []string, done func(error)) error {
	m := Inbound{Inbound: chat.Inbound{JID: jid, Brain: br.ID(), Sender: sender, Text: text, Media: media, Source: chat.SourceIngest, At: time.Now(), Done: done}}
	_, _, _, err := b.brainMgr.Submit(m)
	return err
}

// replyChatFor picks the chat a brain answers in when a message did not come
// from a chat: its configured reply_chat, else your own DM (ownerChat) for
// the brain that gets DMs (the DM brain, or the fallback when there is
// none), else the brain's first group. The DM comes before any group: files
// handed in privately must not land in a group because the brain also
// serves one.
func (b *Bridge) replyChatFor(br *Brain, cfg *config.Config) string {
	m := br.Manifest()
	if m.ReplyChat != "" {
		return m.ReplyChat
	}
	getsDMs := m.Routing.HandlesDMs || b.brainMgr.DMBrain() == br
	return route.ReplyChat(m, getsDMs, ownerChat(cfg))
}
