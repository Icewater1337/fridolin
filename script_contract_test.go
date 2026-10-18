package main

import (
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// The script API, POST /send and POST /ingest, is what local scripts and
// brains' tools call without the dashboard token. These tests pin its
// contract: the tokens it takes, status codes, the JSON of errors and
// successes, how long text is chunked, and the history line written in the
// brain that owns the chat.

// historyLines returns the lines of a brain folder's history file.
func historyLines(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, historyFileName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// scriptCall is a golden file for one request of the script API, sent with
// the scripts token as a local script does: the request, the response, what it
// sent, and the lines it added to the history of each of brains (id →
// folder), under the brain's id, so that a line written to the wrong brain
// shows.
func scriptCall(t *testing.T, h *harness, s *scrubber, brains map[string]string, body string, mutate ...func(*http.Request)) []byte {
	t.Helper()
	ids := slices.Sorted(maps.Keys(brains))
	sentBefore, histBefore := len(h.Outbox()), map[string]int{}
	for _, id := range ids {
		histBefore[id] = len(historyLines(t, brains[id]))
	}
	added := func() (n int) {
		for _, id := range ids {
			n += len(historyLines(t, brains[id])) - histBefore[id]
		}
		return n
	}
	path := "/send"
	if strings.Contains(body, `"brain"`) || strings.Contains(body, `"paths"`) || strings.Contains(body, `"scope"`) {
		path = "/ingest"
	}
	r := httptest.NewRequest("POST", "http://"+defaultHTTPListen+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+h.readToken("scripts.token"))
	for _, m := range mutate {
		m(r)
	}
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, r)
	var sb strings.Builder
	shown := body
	if len(shown) > 300 {
		shown = shown[:300] + fmt.Sprintf("… (%d bytes)", len(body))
	}
	fmt.Fprintf(&sb, "POST %s\n%s\n%s", path, s.text(shown), s.response(t, w))
	if w.Code == http.StatusAccepted {
		// /ingest answers at once and runs the brain afterwards.
		h.WaitFor("the brain's answer", func() bool { return added() >= 2 && len(h.Outbox()) > sentBefore })
	}
	sb.WriteString("\nsent:\n")
	for _, m := range h.Outbox()[sentBefore:] {
		fmt.Fprintf(&sb, "  %s %q\n", m.JID, s.text(m.Text))
	}
	sb.WriteString("history:\n")
	var lines []string
	for _, id := range ids {
		for _, l := range historyLines(t, brains[id])[histBefore[id]:] {
			fmt.Fprintf(&sb, "  %s: %s\n", id, s.text(l))
			lines = append(lines, l)
		}
	}
	assertScriptAPIContract(t, path, body, w, lines)
	return []byte(sb.String())
}

func TestGoldenSendContract(t *testing.T) {
	h := newHarness(t, `{"max_chunk_size": 100}`)
	s := h.scrubber()
	// The DM brain, and a brain that owns the group.
	brains := map[string]string{
		"main":   h.AddBrain(BrainManifest{ID: "main", Name: "Main", Routing: BrainRouting{HandlesDMs: true}}),
		"family": h.AddBrain(BrainManifest{ID: "family", Name: "Family", Routing: BrainRouting{Groups: []string{harnessGroup}}}),
	}
	long := strings.Repeat("All work and no play. ", 12)
	tests := []struct {
		name, body string
		mutate     func(*http.Request)
	}{
		{"sent", `{"jid": "` + harnessDM + `", "text": "hello from a script"}`, nil},
		{"chunked", `{"jid": "` + harnessDM + `", "text": "` + long + `"}`, nil},
		// In the history of the brain that owns the group, not the DM brain's.
		{"group", `{"jid": "` + harnessGroup + `", "text": "to the family"}`, nil},
		// A group no brain lists.
		{"unrouted-group", `{"jid": "120363000000000001@g.us", "text": "to the club"}`, nil},
		{"not-json", `{"jid":`, nil},
		{"no-jid", `{"text": "hello"}`, nil},
		{"blank-text", `{"jid": "` + harnessDM + `", "text": " \n\t"}`, nil},
		{"status-broadcast", `{"jid": "status@broadcast", "text": "hello"}`, nil},
		{"newsletter", `{"jid": "120363000000000000@newsletter", "text": "hello"}`, nil},
		{"too-large", `{"jid": "` + harnessDM + `", "text": "` + strings.Repeat("x", 1<<20) + `"}`, nil},
		{"cross-site", `{"jid": "` + harnessDM + `", "text": "hello"}`, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.com") }},
		{"other-host", `{"jid": "` + harnessDM + `", "text": "hello"}`, func(r *http.Request) { r.Host = "evil.example.com:8765" }},
		// Without a token, as scripts written before tokens send: allowed
		// while script_api.tokenless_send is on, as it is by default.
		{"no-token", `{"jid": "` + harnessDM + `", "text": "hello"}`, withoutToken},
		{"bad-token", `{"jid": "` + harnessDM + `", "text": "hello"}`, withToken("nope")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var muts []func(*http.Request)
			if tc.mutate != nil {
				muts = append(muts, tc.mutate)
			}
			assertGolden(t, "script/send-"+tc.name+".golden", scriptCall(t, h, s, brains, tc.body, muts...))
		})
	}

	t.Run("no-token-tokenless-off", func(t *testing.T) {
		h := newHarness(t, `{"script_api": {"tokenless_send": false}}`)
		brains := map[string]string{"main": h.AddBrain(BrainManifest{ID: "main", Name: "Main", Routing: BrainRouting{HandlesDMs: true}})}
		assertGolden(t, "script/send-no-token-tokenless-off.golden", scriptCall(t, h, h.scrubber(), brains, `{"jid": "`+harnessDM+`", "text": "hello"}`, withoutToken))
	})
}

// withoutToken sends a script API request without a token.
func withoutToken(r *http.Request) { r.Header.Del("Authorization") }

// withToken sends a script API request with tok as its token.
func withToken(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

func TestGoldenIngestContract(t *testing.T) {
	h := newHarness(t, `{"ingest": {"enabled": true}}`)
	s := h.scrubber()
	brains := map[string]string{
		"main": h.AddBrain(BrainManifest{ID: "main", Name: "Main", Routing: BrainRouting{HandlesDMs: true}}),
		// A brain with no chat to answer in.
		"quiet": h.AddBrain(BrainManifest{ID: "quiet", Name: "Quiet"}),
	}
	inbox := filepath.Join(h.DataDir(), "media")
	file := func(name string) string {
		p := filepath.Join(inbox, name)
		if err := os.WriteFile(p, []byte("notes"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	outside := filepath.Join(h.Root(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(inbox, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(inbox, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(inbox, "folder")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, body string
		mutate     []func(*http.Request)
	}{
		{"accepted", `{"brain": "main", "paths": ["` + file("notes.txt") + `"], "text": "File these notes.", "sender": "recorder"}`, nil},
		{"text-only", `{"brain": "Main ", "text": "Just text."}`, nil},
		{"not-json", `{"brain":`, nil},
		{"scope", `{"scope": "main", "text": "x"}`, nil},
		{"nothing", `{"brain": "main"}`, nil},
		{"unknown-brain", `{"brain": "nope", "text": "x"}`, nil},
		{"no-reply-chat", `{"brain": "quiet", "text": "x"}`, nil},
		{"relative-path", `{"brain": "main", "paths": ["notes.txt"]}`, nil},
		{"outside-the-inbox", `{"brain": "main", "paths": ["` + outside + `"]}`, nil},
		{"symlink-out-of-the-inbox", `{"brain": "main", "paths": ["` + link + `"]}`, nil},
		{"missing-file", `{"brain": "main", "paths": ["` + filepath.Join(inbox, "gone.txt") + `"]}`, nil},
		{"fifo", `{"brain": "main", "paths": ["` + fifo + `"]}`, nil},
		{"folder", `{"brain": "main", "paths": ["` + sub + `"]}`, nil},
		// /ingest takes no request without a token.
		{"no-token", `{"brain": "main", "text": "x"}`, []func(*http.Request){withoutToken}},
		{"bad-token", `{"brain": "main", "text": "x"}`, []func(*http.Request){withToken("nope")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertGolden(t, "script/ingest-"+tc.name+".golden", scriptCall(t, h, s, brains, tc.body, tc.mutate...))
		})
	}

	t.Run("disabled", func(t *testing.T) {
		h := newHarness(t, "")
		brains := map[string]string{"main": h.AddBrain(BrainManifest{ID: "main", Name: "Main", Routing: BrainRouting{HandlesDMs: true}})}
		assertGolden(t, "script/ingest-disabled.golden", scriptCall(t, h, h.scrubber(), brains, `{"brain": "main", "text": "x"}`))
	})
}
