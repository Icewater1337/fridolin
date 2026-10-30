package toolcli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/mcp"
)

// liveEnv is the environment of a session's tools: the bridge's address and
// a token file, as the bridge puts them in a run's.
func liveEnv(t *testing.T, url string) IO {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "run.token")
	if err := os.WriteFile(tokenFile, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FRIDOLIN_API": url, "FRIDOLIN_BRAIN_TOKEN_FILE": tokenFile}
	return IO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Getenv: func(k string) string { return env[k] }}
}

// The Stop hook posts the turn's last text with the session's token.
func TestLiveTurnEndPostsTheAnswer(t *testing.T) {
	var got struct{ Text string }
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/brain/live/turn-end" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()
	hook := `{"session_id":"s","hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"Tuesday at 3 works."}`
	t.Run("answer", func(t *testing.T) {
		if code := LiveTurnEnd(context.Background(), strings.NewReader(hook), liveEnv(t, srv.URL)); code != 0 {
			t.Fatalf("exit %d", code)
		}
		if got.Text != "Tuesday at 3 works." || auth != "Bearer tok" {
			t.Errorf("posted %+v with %q", got, auth)
		}
	})
	t.Run("not a hook", func(t *testing.T) {
		if code := LiveTurnEnd(context.Background(), strings.NewReader("nonsense"), liveEnv(t, srv.URL)); code == 0 {
			t.Error("exit 0 for input that is no hook's")
		}
	})
	t.Run("no bridge", func(t *testing.T) {
		env := liveEnv(t, "http://127.0.0.1:1")
		if code := LiveTurnEnd(context.Background(), strings.NewReader(hook), env); code == 0 {
			t.Error("exit 0 with no bridge to tell")
		}
	})
}

// pumpLive turns the bridge's events into channel notifications, asks
// again after a 204, and stops with its context.
func TestPumpLive(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/brain/live/next" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, `{"error":"no"}`, http.StatusUnauthorized)
			return
		}
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusNoContent)
		case 2:
			w.Write([]byte(`{"content":"hello","meta":{"kind":"message","turn":"1"}}`))
		default:
			<-r.Context().Done()
		}
	}))
	defer srv.Close()
	c, err := BrainClientFor(liveEnv(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	c.http.Timeout = 0
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan mcp.Notification)
	done := make(chan struct{})
	go func() { pumpLive(ctx, c, out, t.Logf); close(done) }()
	select {
	case n := <-out:
		ev, ok := n.Params.(liveEvent)
		if n.Method != "notifications/claude/channel" || !ok || ev.Content != "hello" || ev.Meta["turn"] != "1" {
			t.Errorf("notification: %+v", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notification")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pumpLive did not stop with its context")
	}
}
