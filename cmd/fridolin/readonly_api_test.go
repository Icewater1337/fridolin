package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/activity"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/logx"
)

type activityProbe struct {
	limit int
	brain string
}

func (p *activityProbe) Recent(n int, brain string) []activity.Entry {
	p.limit, p.brain = n, brain
	return []activity.Entry{}
}

type logProbe struct {
	after int64
	limit int
}

func (p *logProbe) Since(after int64, n int) ([]logx.Line, int64) {
	p.after, p.limit = after, n
	return nil, 9
}

type readinessProbe string

func (p readinessProbe) State() chat.LinkState { return chat.LinkState{Mode: string(p)} }

func TestReadOnlyActivityLimitsAndFilter(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  int
	}{{"", 100}, {"?limit=-1", 100}, {"?limit=bad", 100}, {"?limit=15", 15}, {"?limit=999", 500}} {
		p := &activityProbe{}
		sep := "&"
		if tc.query == "" {
			sep = "?"
		}
		ActivityAPI{Reader: p}.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/activity"+tc.query+sep+"brain=notes", nil))
		if p.limit != tc.want || p.brain != "notes" {
			t.Fatalf("%s: limit=%d brain=%q", tc.query, p.limit, p.brain)
		}
	}
}
func TestReadOnlyLogsCursorLimitsAndWireShape(t *testing.T) {
	for _, tc := range []struct {
		query string
		after int64
		limit int
	}{{"", 0, 500}, {"?limit=bad&after=bad", 0, 500}, {"?limit=900&after=42", 42, 600}, {"?limit=-1&after=-3", -3, 500}, {"?limit=10", 0, 10}} {
		p := &logProbe{}
		w := httptest.NewRecorder()
		LogsAPI{Reader: p, StartedAt: time.Unix(123, 0), MaxLines: 600}.ServeHTTP(w, httptest.NewRequest("GET", "/api/logs"+tc.query, nil))
		if p.after != tc.after || p.limit != tc.limit {
			t.Fatalf("%s: cursor=%d limit=%d", tc.query, p.after, p.limit)
		}
		if strings.TrimSpace(w.Body.String()) != `{"boot":123,"lines":[],"next":9}` {
			t.Fatal(w.Body.String())
		}
	}
}
func TestReadOnlyReadyModes(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		dev    bool
		status int
	}{{"connected", false, 200}, {"disconnected", false, 503}, {"pairing", false, 503}, {"disconnected", true, 200}} {
		w := httptest.NewRecorder()
		ReadyAPI{Connection: readinessProbe(tc.mode), DevMode: tc.dev, Version: func() string { return "test" }, StartedAt: time.Unix(123, 0).UTC()}.ServeHTTP(w, httptest.NewRequest("GET", "/ready", nil))
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%+v: status=%d headers=%v", tc, w.Code, w.Header())
		}
	}
}
