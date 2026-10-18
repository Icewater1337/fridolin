package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestChatJID(t *testing.T) {
	cases := []struct {
		in, want string // want "" = refused
	}{
		{"15551234567@s.whatsapp.net", "15551234567@s.whatsapp.net"},
		{"15551234567", "15551234567@s.whatsapp.net"},
		{"+1 555 123-4567", "15551234567@s.whatsapp.net"},
		{" 15551234567@s.whatsapp.net ", "15551234567@s.whatsapp.net"},
		{"15551234567:12@s.whatsapp.net", "15551234567@s.whatsapp.net"},
		{"120363000000000000@g.us", "120363000000000000@g.us"},
		{bridgeLID + "@lid", bridgeLID + "@lid"},
		{"status@broadcast", "status@broadcast"}, // a chat, which /send then refuses
		{"12345", ""},
		{"call me", ""},
		{"@s.whatsapp.net", ""},
		{"alex@s.whatsapp.net", ""},
		{"15551234567@example.com", ""},
		{"", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := chatJID(c.in)
			if c.want == "" {
				if err == nil {
					t.Errorf("chatJID(%q) = %v, want an error", c.in, got)
				}
				return
			}
			if err != nil || got.String() != c.want {
				t.Errorf("chatJID(%q) = %v, %v; want %s", c.in, got, err, c.want)
			}
		})
	}
}

func TestIdentityKey(t *testing.T) {
	cases := map[string]string{
		"15551234567":                    "15551234567",
		"+1 555 123 4567":                "15551234567",
		"+1 (555) 123-4567":              "15551234567",
		"+1\u00a0555\u00a0123\u00a04567": "15551234567", // no-break spaces
		"15551234567@s.whatsapp.net":     "15551234567",
		bridgeLID + "@lid":               bridgeLID,
		"Alex":                           "Alex",
	}
	for in, want := range cases {
		if got := identityKey(in); got != want {
			t.Errorf("identityKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// A number written into config.json by hand, the way people write it,
// lets its sender in; the running config holds it as digits.
func TestLoadConfigNormalisesAllowedNumbers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg, err := LoadConfig(writeConfig(t, `{"data_dir": "`+home+`/data", "allowed_numbers": ["+1 555 123-4567", "15559876543", "call me"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"15551234567", "15559876543", "call me"}; !slices.Equal(cfg.AllowedNumbers, want) {
		t.Errorf("allowed_numbers = %q, want %q", cfg.AllowedNumbers, want)
	}
	if !numberAllowed(cfg, "15551234567") {
		t.Error("the hand-written number is refused")
	}
	if got := badAllowedNumbers(cfg.AllowedNumbers); !slices.Equal(got, []string{"call me"}) {
		t.Errorf("badAllowedNumbers = %q, want the entry that is no number", got)
	}
}

// The Overview says when allowed_numbers holds an entry that lets nobody in.
func TestStatusWarnsAboutAnAllowedNumberThatIsNone(t *testing.T) {
	tb := newTestBridge(t, `, "allowed_numbers": ["15551234567", "my phone"]`)
	var st statusResponse
	if err := json.Unmarshal(tb.do("GET", "/api/status", nil).Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(st.Warnings, func(w string) bool { return strings.Contains(w, `"my phone"`) }) {
		t.Errorf("warnings = %q, want one naming the entry", st.Warnings)
	}
}

// /send takes a bare number for the DM with it, and records what it sent
// under the chat's JID, where the brain's next run looks; a chat that is
// none is the script's mistake, a 400, not a failed send.
func TestHTTPSendTakesANumber(t *testing.T) {
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, BrainManifest{ID: "p", Name: "P", Routing: BrainRouting{HandlesDMs: true}})
	br := tb.brainMgr.Get("p")
	send := func(jid string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8765/send", strings.NewReader(`{"jid":"`+jid+`","text":"from a script"}`))
		r.Host = "127.0.0.1:8765"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		tb.h.ServeHTTP(w, r)
		return w
	}
	if w := send("+1 555 123 4567"); w.Code != http.StatusOK {
		t.Fatalf("send to a number: %d %s", w.Code, w.Body)
	}
	if got := tb.sentTo(testDMChat); !slices.Equal(got, []string{"from a script"}) {
		t.Errorf("sent to %s: %q", testDMChat, got)
	}
	h := readHistory(t, br)
	if len(h) != 1 || h[0].Chat != testDMChat || h[0].Type != historyOut {
		t.Errorf("history = %+v, want one outbound line in %s", h, testDMChat)
	}
	for _, bad := range []string{"12345", "alex@s.whatsapp.net", "15551234567@example.com"} {
		if w := send(bad); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not a chat") {
			t.Errorf("send to %q: %d %s, want 400", bad, w.Code, w.Body)
		}
	}
}
