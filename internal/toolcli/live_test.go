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
	var got struct{ Text, Turn string }
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
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"user","message":{"role":"user","content":"<channel source=\"fridolin\" kind=\"message\" turn=\"4\">\nhi\n</channel>"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hookWith := func(path string) string {
		data, _ := json.Marshal(map[string]any{"session_id": "s", "hook_event_name": "Stop", "stop_hook_active": false, "last_assistant_message": "Tuesday at 3 works.", "transcript_path": path})
		return string(data)
	}
	hook := hookWith(transcript)
	t.Run("answer", func(t *testing.T) {
		if code := LiveTurnEnd(context.Background(), strings.NewReader(hook), liveEnv(t, srv.URL)); code != 0 {
			t.Fatalf("exit %d", code)
		}
		if got.Text != "Tuesday at 3 works." || got.Turn != "4" || auth != "Bearer tok" {
			t.Errorf("posted %+v with %q", got, auth)
		}
	})
	t.Run("no transcript", func(t *testing.T) {
		// The text still reaches the bridge, with no turn it could answer.
		if code := LiveTurnEnd(context.Background(), strings.NewReader(hookWith(filepath.Join(t.TempDir(), "gone"))), liveEnv(t, srv.URL)); code != 0 {
			t.Fatalf("exit %d", code)
		}
		if got.Text == "" || got.Turn != "" {
			t.Errorf("posted %+v", got)
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

// Only what the model was given acks a turn: a message still in the queue,
// a tool's result or a message quoting the tag do not (#558).
func TestLastLiveTurn(t *testing.T) {
	tag := func(turn string) string {
		return `<channel source=\"fridolin\" kind=\"message\" turn=\"` + turn + `\">\nhi\n</channel>`
	}
	user := `{"type":"user","message":{"role":"user","content":"` + tag("3") + `"}}`
	for _, tc := range []struct {
		name, lines, want string
	}{
		{"none", `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`, ""},
		{"a turn started with it", user, "3"},
		{"as blocks", `{"type":"user","message":{"content":[{"type":"text","text":"` + tag("3") + `"}]}}`, "3"},
		{"taken in by the running turn", `{"type":"attachment","attachment":{"type":"queued_command","prompt":"` + tag("3") + `"}}`, "3"},
		{"the last one counts", user + "\n" + `{"type":"user","message":{"content":"` + tag("4") + `"}}`, "4"},
		{"still queued", user + "\n" + `{"type":"queue-operation","operation":"enqueue","content":"` + tag("4") + `"}`, "3"},
		{"a tool's result", user + "\n" + `{"type":"user","message":{"content":[{"type":"tool_result","content":"` + tag("4") + `"}]}}`, "3"},
		{"quoted in a message", user + "\n" + `{"type":"user","message":{"content":"see ` + tag("4") + `"}}`, "3"},
		{"another server", user + "\n" + `{"type":"user","message":{"content":"<channel source=\"other\" turn=\"4\">"}}`, "3"},
		{"a line being written", user + "\n" + `{"type":"user","message":{"content":"` + tag("4"), "3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, []byte(tc.lines), 0o600); err != nil {
				t.Fatal(err)
			}
			if got, err := lastLiveTurn(path); err != nil || got != tc.want {
				t.Errorf("got %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}
