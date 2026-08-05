package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// sendRequest is the JSON body expected by POST /send.
type sendRequest struct {
	JID  string `json:"jid"`
	Text string `json:"text"`
}

// startHTTPServer starts the local outbound-send API in a background goroutine.
//
// The API has no authentication, so it must stay bound to a loopback address:
// anything that can reach it can send WhatsApp messages as you. A non-loopback
// listen address is refused rather than silently exposed.
func (b *Bridge) startHTTPServer(addr string) {
	if !isLoopbackAddr(addr) {
		log.Printf("REFUSING to start HTTP API on %q: the API is unauthenticated and must bind to localhost "+
			"(use 127.0.0.1:8765, or put a reverse proxy with auth in front of it)", addr)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/send", b.httpSend)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("HTTP API listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()
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

// httpSend handles POST /send {"jid":"…","text":"…"}.
//
// The sent message is appended to the owning brain's history so the brain can
// see what was said on its behalf — for example, so the OTP guard recognises a
// code request that a script sent out.
func (b *Bridge) httpSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req sendRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.JID == "" || req.Text == "" {
		http.Error(w, "jid and text are required", http.StatusBadRequest)
		return
	}

	if err := b.wa.SendChunked(context.Background(), req.JID, req.Text); err != nil {
		log.Printf("HTTP send error to %s: %v", req.JID, err)
		http.Error(w, "send failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	b.appendHTTPMessageToHistory(req.JID, req.Text)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "sent"})
}

// appendHTTPMessageToHistory records an HTTP-sent message in the history of
// whichever brain owns the target chat. No-op in classic mode.
func (b *Bridge) appendHTTPMessageToHistory(jid, text string) {
	if b.brainMgr == nil {
		return
	}
	br := b.brainMgr.BrainFor(jid, strings.HasSuffix(jid, "@g.us"))
	if br == nil {
		return
	}
	br.appendHistory(jid, br.name, text, "out")
}
