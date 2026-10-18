package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
)

// script sends a request to the script API the way a local script does.
func (tb *testBridge) script(method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8765"+path, strings.NewReader(body))
	r.Host = "127.0.0.1:8765"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	tb.h.ServeHTTP(w, r)
	return w
}

// The contract of /health, /send and /ingest (docs/API.md): every status
// code and the shape of every body. Errors are JSON {"error": ...}.
func TestScriptAPIContract(t *testing.T) {
	tb := newTestBridge(t, `, "ingest": {"enabled": true}`)
	tb.addFolderBrain(t, BrainManifest{ID: "personal", Name: "Personal", Routing: BrainRouting{HandlesDMs: true}})
	media := tb.cfgs.Get().MediaDir
	inside := filepath.Join(media, "scan.pdf")
	if err := os.WriteFile(inside, []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(tb.home, "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	const dm = "15551234567@s.whatsapp.net"
	cases := []struct {
		name, path, body string
		want             int
		fields           map[string]string // in the JSON body; "error" is matched as a substring
	}{
		{"send", "/send", `{"jid":"` + dm + `","text":"hello"}`, 200, map[string]string{"status": "sent"}},
		{"send to a group", "/send", `{"jid":"120363000000000000@g.us","text":"hello"}`, 200, map[string]string{"status": "sent"}},
		{"send not JSON", "/send", `jid=x`, 400, map[string]string{"error": "invalid JSON"}},
		{"send without jid", "/send", `{"text":"hello"}`, 400, map[string]string{"error": "jid and text are required"}},
		{"send blank text", "/send", `{"jid":"` + dm + `","text":" "}`, 400, map[string]string{"error": "jid and text are required"}},
		{"send to no chat", "/send", `{"jid":"hello","text":"x"}`, 400, map[string]string{"error": "not a chat JID"}},
		{"send to an email", "/send", `{"jid":"you@example.com","text":"x"}`, 400, map[string]string{"error": "not a chat JID"}},
		{"send without a user", "/send", `{"jid":"@s.whatsapp.net","text":"x"}`, 400, map[string]string{"error": "not a chat JID"}},
		{"send to Status", "/send", `{"jid":"status@broadcast","text":"x"}`, 400, map[string]string{"error": "refusing to send"}},
		{"send to a channel", "/send", `{"jid":"120363000000000000@newsletter","text":"x"}`, 400, map[string]string{"error": "refusing to send"}},
		{"send over 1 MiB", "/send", `{"jid":"` + dm + `","text":"` + strings.Repeat("a", 1<<20) + `"}`, 400, map[string]string{"error": "too large"}},
		{"ingest", "/ingest", `{"brain":"personal","paths":["` + inside + `"],"text":"file this"}`, 202, map[string]string{"status": "accepted", "jid": dm}},
		{"ingest by scope", "/ingest", `{"scope":"personal","text":"note"}`, 400, map[string]string{"error": `name the brain in "brain"`}},
		{"ingest nothing", "/ingest", `{"brain":"personal"}`, 400, map[string]string{"error": "paths or text required"}},
		{"ingest unknown brain", "/ingest", `{"brain":"nobody","text":"x"}`, 400, map[string]string{"error": `unknown brain "nobody"`}},
		{"ingest outside media", "/ingest", `{"brain":"personal","paths":["` + outside + `"]}`, 400, map[string]string{"error": "inside the media directory"}},
		{"ingest relative path", "/ingest", `{"brain":"personal","paths":["scan.pdf"]}`, 400, map[string]string{"error": "inside the media directory"}},
		{"ingest a folder", "/ingest", `{"brain":"personal","paths":["` + media + `"]}`, 400, map[string]string{"error": "not a regular file"}},
		{"ingest not JSON", "/ingest", `{`, 400, map[string]string{"error": "invalid JSON"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := tb.script("POST", c.path, c.body)
			var got map[string]string
			if w.Code != c.want || w.Header().Get("Content-Type") != "application/json" || json.Unmarshal(w.Body.Bytes(), &got) != nil {
				t.Fatalf("%d %s %q, want %d with a JSON body", w.Code, w.Header().Get("Content-Type"), w.Body.String(), c.want)
			}
			for k, v := range c.fields {
				if k == "error" && !strings.Contains(got[k], v) || k != "error" && got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
		})
	}

	if w := tb.script("GET", "/health", ""); w.Code != 200 || w.Body.String() != "ok\n" {
		t.Errorf("health: %d %q", w.Code, w.Body.String())
	}
	var ready map[string]any
	if w := tb.script("GET", "/ready", ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ready) != nil ||
		ready["ready"] != true || ready["whatsapp"] == nil || ready["version"] == nil || ready["started_at"] == nil {
		t.Errorf("ready in dev mode: %d %s", w.Code, w.Body.String())
	}

	// /send takes text as it is: a [POLL] block is not made a poll.
	poll := "[POLL]\nQuestion: Lunch?\n- Yes\n- No\n[/POLL]"
	body, _ := json.Marshal(sendRequest{JID: dm, Text: poll})
	if w := tb.script("POST", "/send", string(body)); w.Code != 200 {
		t.Fatalf("send a poll block: %d %s", w.Code, w.Body.String())
	}
	// The ingested files' runs send to the same chat, so look for it.
	sent := false
	for _, m := range tb.dev.Outbox() {
		if m.Text == poll && m.Poll == nil {
			sent = true
		}
		if m.Poll != nil {
			t.Errorf("a poll was sent: %+v", m)
		}
	}
	if !sent {
		t.Errorf("outbox = %+v, want the block as plain text", tb.dev.Outbox())
	}

	off := newTestBridge(t, "")
	if w := off.script("POST", "/ingest", `{"brain":"personal","text":"x"}`); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"error"`) {
		t.Errorf("ingest while disabled: %d %s", w.Code, w.Body.String())
	}
}

// failingMessenger is the dev messenger with SendChunked failing as err.
type failingMessenger struct {
	*devMessenger
	err error
}

func (f failingMessenger) SendChunked(context.Context, string, string) error { return f.err }

// A send that WhatsApp refused gets a status that says whose problem it is,
// and what did go out before a failure is in the history.
func TestSendFailureStatus(t *testing.T) {
	const dm = "15551234567@s.whatsapp.net"
	cases := []struct {
		name string
		err  error
		want int
		hist string
	}{
		{"not connected", classifySendErr(whatsmeow.ErrNotConnected), http.StatusServiceUnavailable, ""},
		{"not paired", classifySendErr(whatsmeow.ErrNotLoggedIn), http.StatusServiceUnavailable, ""},
		{"unknown server", classifySendErr(whatsmeow.ErrUnknownServer), http.StatusBadRequest, ""},
		{"anything else", errors.New("boom"), http.StatusInternalServerError, ""},
		{"part way", &partialSendError{sent: []string{"[1/2] first half"}, total: 2, err: classifySendErr(whatsmeow.ErrNotConnected)}, http.StatusServiceUnavailable, "[1/2] first half"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb := newTestBridge(t, "")
			dir := tb.addFolderBrain(t, BrainManifest{ID: "p", Name: "P", Routing: BrainRouting{HandlesDMs: true}})
			tb.wa = failingMessenger{devMessenger: tb.dev, err: c.err}
			w := tb.script("POST", "/send", `{"jid":"`+dm+`","text":"first half second half"}`)
			if w.Code != c.want || !strings.Contains(w.Body.String(), `"error"`) {
				t.Fatalf("%d %s, want %d", w.Code, w.Body.String(), c.want)
			}
			hist, _ := os.ReadFile(filepath.Join(dir, "conversation-history.jsonl"))
			if c.hist == "" && len(hist) != 0 || c.hist != "" && !strings.Contains(string(hist), c.hist) {
				t.Errorf("history = %q, want %q", hist, c.hist)
			}
		})
	}
}

func TestSendChunksReportsWhatWentOut(t *testing.T) {
	chunks := []string{"a", "b", "c"}
	fail := errors.New("offline")
	for i := range chunks {
		t.Run(fmt.Sprint("fails at ", i), func(t *testing.T) {
			var sent []string
			err := sendChunks(chunks, 0, func(c string) error {
				if len(sent) == i {
					return fail
				}
				sent = append(sent, c)
				return nil
			})
			var partial *partialSendError
			switch {
			case !errors.Is(err, fail):
				t.Fatalf("err = %v", err)
			case i == 0 && errors.As(err, &partial):
				t.Fatalf("nothing went out, yet %v", err)
			case i > 0 && (!errors.As(err, &partial) || strings.Join(partial.sent, "") != strings.Join(chunks[:i], "")):
				t.Fatalf("err = %v, want the first %d chunks reported", err, i)
			}
		})
	}
	if err := sendChunks(chunks, 0, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
