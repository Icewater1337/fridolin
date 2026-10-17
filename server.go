package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sendRequest is the JSON body expected by POST /send.
type sendRequest struct {
	JID  string `json:"jid"`
	Text string `json:"text"`
}

// ingestRequest is the JSON body expected by POST /ingest. Brain names the
// brain; Paths are files already written inside the media directory; Text
// is the message that goes with them.
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

// isLoopbackAddr reports whether addr binds to a loopback interface.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
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
		Version:   bridgeVersion(),
		StartedAt: b.started,
	}
	status := http.StatusOK
	if !resp.Ready {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, resp)
}

// httpSend handles POST /send {"jid":"…","text":"…"} for local scripts.
//
// The sent message is appended to the owning brain's history so the brain can
// see what was said on its behalf — for example, so the OTP guard recognises a
// code request that a script sent out.
func (b *Bridge) httpSend(w http.ResponseWriter, r *http.Request) {
	var req sendRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Blank text would send nothing (ChunkMessage gives no chunks for it),
	// so refuse it rather than answer "sent" and log a message to history.
	if req.JID == "" || strings.TrimSpace(req.Text) == "" {
		http.Error(w, "jid and text are required", http.StatusBadRequest)
		return
	}
	// Refused before sending: a script gets a 400 for its own mistake rather
	// than a failed send, and dev mode, whose messenger takes any JID,
	// refuses it too.
	if _, err := parseRecipient(req.JID); errors.Is(err, errRefusedRecipient) {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := b.wa.SendChunked(r.Context(), req.JID, req.Text); err != nil {
		log.Printf("HTTP send error to %s: %v", req.JID, err)
		http.Error(w, "send failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	b.appendHTTPMessageToHistory(req.JID, req.Text)
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// appendHTTPMessageToHistory records an HTTP-sent message in the history of
// whichever brain owns the target chat.
func (b *Bridge) appendHTTPMessageToHistory(jid, text string) {
	br := b.brainMgr.BrainFor(jid, strings.HasSuffix(jid, "@g.us"))
	if br == nil {
		return
	}
	br.appendHistory(jid, br.id, text, historyOut)
}

// httpIngest handles POST /ingest. It feeds files that arrived outside
// WhatsApp (a web upload, a recorder sync) through the same pipeline as a
// WhatsApp attachment. They run in the brain the request names, and the
// brain's reply goes to its reply chat, even when that chat routes to another
// brain. Responds 202 at once; processing is asynchronous.
func (b *Bridge) httpIngest(w http.ResponseWriter, r *http.Request) {
	cfg := b.cfgs.Get()
	if !cfg.Ingest.Enabled {
		http.Error(w, "ingest is disabled (set ingest.enabled in the dashboard)", http.StatusNotFound)
		return
	}
	var req ingestRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Scope != "" {
		http.Error(w, `"scope" is no longer read: name the brain in "brain"`, http.StatusBadRequest)
		return
	}
	if len(req.Paths) == 0 && strings.TrimSpace(req.Text) == "" {
		http.Error(w, "paths or text required", http.StatusBadRequest)
		return
	}
	id := strings.ToLower(strings.TrimSpace(req.Brain))
	br := b.brainMgr.Get(id)
	if br == nil {
		http.Error(w, fmt.Sprintf("unknown brain %q", id), http.StatusBadRequest)
		return
	}

	media := canonicalDir(cfg.MediaDir)
	var paths []string
	for _, p := range req.Paths {
		resolved, err := filepath.EvalSymlinks(p)
		if !filepath.IsAbs(p) || err != nil || !isWithin(resolved, media) {
			http.Error(w, "every path must be an existing file inside the media directory", http.StatusBadRequest)
			return
		}
		if st, err := os.Stat(resolved); err != nil || !st.Mode().IsRegular() {
			http.Error(w, "not a regular file: "+filepath.Base(p), http.StatusBadRequest)
			return
		}
		paths = append(paths, resolved)
	}

	jid := b.replyChatFor(br, cfg)
	if jid == "" {
		http.Error(w, "brain "+br.ID()+" has no chat to reply in — set reply_chat in its settings", http.StatusBadRequest)
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
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "jid": jid})
}

// handOff feeds files that arrived outside WhatsApp to the brain picked for
// them, for /ingest and the Plaud integration. The message names the brain,
// so it runs there even when jid, the chat that gets the answer, belongs to
// another brain.
func (b *Bridge) handOff(br *Brain, jid, sender, text string, media []string) {
	m := Inbound{JID: jid, Brain: br.ID(), Sender: sender, Text: text, Media: media, Source: sourceIngest, At: time.Now()}
	b.handleInbound(context.Background(), m)
}

// replyChatFor picks the chat a brain answers in when a message did not come
// from a chat: its configured reply_chat, else the first allowed number for
// the brain that gets DMs (the DM brain, or the fallback when there is none),
// else the brain's first group. The DM comes before any group: files handed
// in privately must not land in a group because the brain also serves one.
func (b *Bridge) replyChatFor(br *Brain, cfg *Config) string {
	m := br.settings.Load().manifest
	if m.ReplyChat != "" {
		return m.ReplyChat
	}
	getsDMs := m.Routing.HandlesDMs || b.brainMgr.BrainFor("", false) == br
	if getsDMs && len(cfg.AllowedNumbers) > 0 {
		return cfg.AllowedNumbers[0] + "@s.whatsapp.net"
	}
	if len(m.Routing.Groups) > 0 {
		return m.Routing.Groups[0]
	}
	return ""
}
