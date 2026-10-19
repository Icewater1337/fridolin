package main

import (
	"github.com/Icewater1337/fridolin/internal/activity"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/logx"
	"net/http"
	"strconv"
	"time"
)

// ActivityReader supplies the current feed without exposing its storage.
type ActivityReader interface {
	Recent(int, string) []activity.Entry
}

// ActivityAPI serves the activity feed with the existing polling validator.
type ActivityAPI struct{ Reader ActivityReader }

// LogReader supplies the current log cursor and matching lines.
type LogReader interface {
	Since(int64, int) ([]logx.Line, int64)
}

// LogsAPI serves process logs with a fixed startup marker and line cap.
type LogsAPI struct {
	Reader    LogReader
	StartedAt time.Time
	MaxLines  int
}

// logsResponse keeps the sorted map-key order and non-null lines wire shape.
type logsResponse struct {
	Boot  int64       `json:"boot"`
	Lines []logx.Line `json:"lines"`
	Next  int64       `json:"next"`
}

// ConnectionState supplies only readiness, without transport operations.
type ConnectionState interface{ State() chat.LinkState }

// ReadyAPI reports whether this process can deliver messages.
type ReadyAPI struct {
	Connection ConnectionState
	DevMode    bool
	Version    func() string
	StartedAt  time.Time
}

func (a ActivityAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)
	httpapi.WriteJSONIfChanged(w, r, a.Reader.Recent(limit, r.URL.Query().Get("brain")))
}

func (a LogsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 500
	}
	limit = min(limit, a.MaxLines)
	lines, next := a.Reader.Since(after, limit)
	if lines == nil {
		lines = []logx.Line{}
	}
	httpapi.WriteJSON(w, http.StatusOK, logsResponse{Boot: a.StartedAt.Unix(), Lines: lines, Next: next})
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
func (a ReadyAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mode := a.Connection.State().Mode
	resp := readyResponse{
		Ready:     mode == "connected" || a.DevMode,
		WhatsApp:  mode,
		Version:   a.Version(),
		StartedAt: a.StartedAt,
	}
	status := http.StatusOK
	if !resp.Ready {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, status, resp)
}
