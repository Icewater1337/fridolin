package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

func (b *Bridge) buildAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/site/build/{id}", b.apiBuildChat)
	mux.HandleFunc("POST /api/site/build/{id}", b.apiBuildSend)
	mux.HandleFunc("POST /api/site/build/{id}/new", b.apiBuildNew)
	mux.HandleFunc("POST /api/site/build/{id}/stop", b.apiBuildStop)
}

// buildBrain returns the builder brain a build request names.
func (b *Bridge) buildBrain(w http.ResponseWriter, r *http.Request) *brain.Brain {
	id := r.PathValue("id")
	br := b.brainMgr.Get(id)
	if br == nil || !b.site.IsBuilder(id) {
		httpapi.WriteErr(w, http.StatusNotFound, "no builder brain "+id+" (Site → Builders)")
		return nil
	}
	return br
}

func (b *Bridge) apiBuildChat(w http.ResponseWriter, r *http.Request) {
	br := b.buildBrain(w, r)
	if br == nil {
		return
	}
	ch := b.site.chats.get(br.ID())
	jid := buildChatJID(br.ID(), ch.Thread)
	rt := br.Snapshot()
	running := rt.Running != nil && rt.Running.Chat == jid
	queued := 0
	for _, q := range rt.Queue {
		if q.Chat == jid {
			queued++
		}
	}
	resp := map[string]any{"messages": ch.Messages, "thread": ch.Thread, "running": running, "queued": queued, "activity": []string{}}
	if running {
		resp["activity"] = currentRunLog(br.StreamLogPath(), rt.Running.PID)
	}
	if p := b.site.previewOf(br.ID()); p != nil {
		resp["preview"] = b.site.previewURLs(br.ID(), p)
	}
	for _, p := range b.site.Settings().Proposals {
		if p.Brain == br.ID() {
			resp["proposal"] = p
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

func (b *Bridge) apiBuildSend(w http.ResponseWriter, r *http.Request) {
	br := b.buildBrain(w, r)
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
	ch := b.site.chats.get(br.ID())
	if err := b.site.chats.add(br.ID(), "user", text); err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	m := brain.Inbound{Inbound: chat.Inbound{JID: buildChatJID(br.ID(), ch.Thread), Sender: "dashboard", Brain: br.ID(),
		Text: text, Source: "site-build", At: time.Now()},
	}
	if _, _, _, err := b.brainMgr.Submit(m); err != nil {
		_ = b.site.chats.add(br.ID(), "assistant", outboundmsg.BuildChatError(err))
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (b *Bridge) apiBuildNew(w http.ResponseWriter, r *http.Request) {
	br := b.buildBrain(w, r)
	if br == nil {
		return
	}
	if err := b.site.chats.newThread(br.ID()); err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (b *Bridge) apiBuildStop(w http.ResponseWriter, r *http.Request) {
	br := b.buildBrain(w, r)
	if br == nil {
		return
	}
	ch := b.site.chats.get(br.ID())
	rt := br.Snapshot()
	if rt.Running == nil || rt.Running.Chat != buildChatJID(br.ID(), ch.Thread) {
		httpapi.WriteErr(w, http.StatusConflict, "nothing is running in this chat")
		return
	}
	br.StopCurrent(false)
	httpapi.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
