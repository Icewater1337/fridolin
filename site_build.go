package main

// The Build page (docs/SITE.md): a chat with a builder brain in the
// dashboard, next to its preview.
//
// A build chat is an ordinary brain run in a chat of its own: the JID
// site-build-<brain>-<n>@fridolin.dashboard, so it gets its own Claude
// session, and the brain's sandbox, plugin and MCP server as on WhatsApp.
// Whatever the brain sends to such a JID lands here instead of WhatsApp.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

const buildChatSuffix = "@fridolin.dashboard"

// BuildMessage is one line of a build chat.
type BuildMessage struct {
	Role string `json:"role"` // user, assistant
	Text string `json:"text"`
	At   string `json:"at"`
}

type buildChat struct {
	Thread   int            `json:"thread"`
	Messages []BuildMessage `json:"messages"`
}

// siteChats keeps the build chats, one per brain, in <data_dir>/site/build/.
type siteChats struct {
	m  *SiteManager
	mu sync.Mutex
}

// path is the file of a brain's build chat. The id arrives in a request
// URL, so only a valid brain id names a file (and so one in this folder).
func (c *siteChats) path(brainID string) (string, error) {
	if !brainIDRe.MatchString(brainID) {
		return "", fmt.Errorf("%q is not a brain id", brainID)
	}
	return c.m.dir("build", brainID+".json"), nil
}

func (c *siteChats) loadLocked(brainID string) buildChat {
	ch := buildChat{Thread: 1, Messages: []BuildMessage{}}
	if p, err := c.path(brainID); err == nil {
		if data, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(data, &ch)
		}
	}
	if ch.Thread < 1 {
		ch.Thread = 1
	}
	if ch.Messages == nil {
		ch.Messages = []BuildMessage{}
	}
	return ch
}

func (c *siteChats) saveLocked(brainID string, ch buildChat) error {
	p, err := c.path(brainID)
	if err != nil {
		return err
	}
	if len(ch.Messages) > 300 {
		ch.Messages = ch.Messages[len(ch.Messages)-300:]
	}
	data, _ := json.MarshalIndent(ch, "", "  ")
	return writeFileAtomic(p, data, 0o600, false)
}

func (c *siteChats) get(brainID string) buildChat {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loadLocked(brainID)
}

func (c *siteChats) add(brainID, role, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.loadLocked(brainID)
	ch.Messages = append(ch.Messages, BuildMessage{Role: role, Text: text, At: time.Now().UTC().Format(time.RFC3339)})
	return c.saveLocked(brainID, ch)
}

// newThread starts a new conversation (a new JID, so a new Claude session).
func (c *siteChats) newThread(brainID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.loadLocked(brainID)
	return c.saveLocked(brainID, buildChat{Thread: ch.Thread + 1, Messages: []BuildMessage{}})
}

func buildChatJID(brainID string, thread int) string {
	return fmt.Sprintf("site-build-%s-%d%s", brainID, thread, buildChatSuffix)
}

// brainOfBuildJID is the brain a build chat JID belongs to, or "".
func brainOfBuildJID(jid string) string {
	rest, ok := strings.CutSuffix(jid, buildChatSuffix)
	if !ok {
		return ""
	}
	rest, ok = strings.CutPrefix(rest, "site-build-")
	if !ok {
		return ""
	}
	i := strings.LastIndex(rest, "-")
	if i <= 0 {
		return ""
	}
	return rest[:i]
}

// siteChatMessenger sends what brains say in build chats to the Build page,
// everything else on as before.
type siteChatMessenger struct {
	Messenger
	chats *siteChats
}

func (s siteChatMessenger) SendMessage(ctx context.Context, jid, text string) error {
	if id := brainOfBuildJID(jid); id != "" {
		return s.toChat(id, text)
	}
	return s.Messenger.SendMessage(ctx, jid, text)
}

func (s siteChatMessenger) SendChunked(ctx context.Context, jid, text string) error {
	if id := brainOfBuildJID(jid); id != "" {
		return s.toChat(id, text)
	}
	return s.Messenger.SendChunked(ctx, jid, text)
}

// TakesWhole reports that a build chat shows a reply in one piece, as
// SendChunked sends it: the Build page has no limit to split for.
func (s siteChatMessenger) TakesWhole(jid string) bool { return brainOfBuildJID(jid) != "" }

func (s siteChatMessenger) SendPoll(ctx context.Context, jid, q string, opts []string, max int) (string, error) {
	if id := brainOfBuildJID(jid); id != "" {
		return "", s.toChat(id, q+"\n"+"• "+strings.Join(opts, "\n• "))
	}
	return s.Messenger.SendPoll(ctx, jid, q, opts, max)
}

func (s siteChatMessenger) toChat(brainID, text string) error {
	// "Working on it" is what the page shows anyway while a run lasts. Only
	// that notice: others start with the same ⏳, and a build chat must not
	// miss that its message waits, or that Claude's usage limit is reached.
	if text == workingNotice {
		return nil
	}
	return s.chats.add(brainID, "assistant", text)
}

// ── API ────────────────────────────────────────────────────────────────────

func (b *Bridge) buildAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/site/build/{id}", b.apiBuildChat)
	mux.HandleFunc("POST /api/site/build/{id}", b.apiBuildSend)
	mux.HandleFunc("POST /api/site/build/{id}/new", b.apiBuildNew)
	mux.HandleFunc("POST /api/site/build/{id}/stop", b.apiBuildStop)
}

// buildBrain returns the builder brain a build request names.
func (b *Bridge) buildBrain(w http.ResponseWriter, r *http.Request) *Brain {
	id := r.PathValue("id")
	br := b.brainMgr.Get(id)
	if br == nil || !b.site.IsBuilder(id) {
		writeErr(w, http.StatusNotFound, "no builder brain "+id+" (Site → Builders)")
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
		resp["activity"] = currentRunLog(br.streamLogPath())
	}
	if p := b.site.previewOf(br.ID()); p != nil {
		resp["preview"] = b.site.previewURLs(br.ID(), p)
	}
	for _, p := range b.site.Settings().Proposals {
		if p.Brain == br.ID() {
			resp["proposal"] = p
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (b *Bridge) apiBuildSend(w http.ResponseWriter, r *http.Request) {
	br := b.buildBrain(w, r)
	if br == nil {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || len(text) > 20000 {
		writeErr(w, http.StatusBadRequest, "write a message (up to 20,000 characters)")
		return
	}
	ch := b.site.chats.get(br.ID())
	if err := b.site.chats.add(br.ID(), "user", text); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	m := Inbound{
		JID: buildChatJID(br.ID(), ch.Thread), Sender: "dashboard", Brain: br.ID(),
		Text: text, Source: "site-build", At: time.Now(),
	}
	if _, _, _, err := b.brainMgr.Submit(m); err != nil {
		_ = b.site.chats.add(br.ID(), "assistant", buildChatError(err))
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (b *Bridge) apiBuildNew(w http.ResponseWriter, r *http.Request) {
	br := b.buildBrain(w, r)
	if br == nil {
		return
	}
	if err := b.site.chats.newThread(br.ID()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (b *Bridge) apiBuildStop(w http.ResponseWriter, r *http.Request) {
	br := b.buildBrain(w, r)
	if br == nil {
		return
	}
	ch := b.site.chats.get(br.ID())
	rt := br.Snapshot()
	if rt.Running == nil || rt.Running.Chat != buildChatJID(br.ID(), ch.Thread) {
		writeErr(w, http.StatusConflict, "nothing is running in this chat")
		return
	}
	br.StopCurrent(false)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// currentRunLog is the brain's activity log since the run in flight began:
// "🔧 Tool: …" and "💬 …" lines, the last 40.
func currentRunLog(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return []string{}
	}
	defer f.Close()
	const window = 64 * 1024
	if st, err := f.Stat(); err == nil && st.Size() > window {
		_, _ = f.Seek(st.Size()-window, io.SeekStart)
	}
	data, _ := io.ReadAll(f)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	start := 0
	for i, l := range lines {
		if strings.Contains(l, "New invocation") {
			start = i + 1
		}
	}
	out := slices.Clone(lines[start:])
	if len(out) > 40 {
		out = out[len(out)-40:]
	}
	return out
}
