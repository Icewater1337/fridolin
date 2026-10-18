package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	tokenTestDM     = "15551234567@s.whatsapp.net"
	tokenTestGroupA = "120363000000000000@g.us"
	tokenTestGroupB = "120363000000000001@g.us"
	tokenTestGroupC = "120363000000000002@g.us" // allowed, claimed by no brain
)

// tokenBridge has brain a (your DMs and group A, and the fallback, as the
// first brain) and brain b (group B), with ingest on and groups A, B and C
// allowed.
func tokenBridge(t *testing.T, extra string) *testBridge {
	t.Helper()
	tb := newTestBridge(t, `, "ingest": {"enabled": true}, "allowed_groups": ["`+tokenTestGroupA+`", "`+tokenTestGroupB+`", "`+tokenTestGroupC+`"]`+extra)
	tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A", Routing: BrainRouting{HandlesDMs: true, Groups: []string{tokenTestGroupA}}})
	tb.addFolderBrain(t, BrainManifest{ID: "b", Name: "B", Routing: BrainRouting{Groups: []string{tokenTestGroupB}}})
	return tb
}

// A brain's run is never told the scripts token or the dashboard token: it
// calls /send and /ingest with its run's token (brainapi.go).
func TestBrainRunsAreNotToldTheScriptsToken(t *testing.T) {
	envLog := filepath.Join(t.TempDir(), "brain.env")
	t.Setenv("FRIDOLIN_FAKE_ENV_LOG", envLog)
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A", Routing: BrainRouting{HandlesDMs: true}})
	if w := tb.do("POST", "/api/dev/inbound", map[string]any{"jid": tokenTestDM, "text": "hi"}); w.Code != 202 {
		t.Fatalf("inbound: %d %s", w.Code, w.Body.String())
	}
	waitFor(t, 5*time.Second, "the run's environment", func() bool { _, err := os.Stat(envLog); return err == nil })
	data, _ := os.ReadFile(envLog)
	if strings.Contains(string(data), tb.scriptAuth.scripts.Value()) || strings.Contains(string(data), tb.token.Value()) {
		t.Error("the run was told the scripts or the dashboard token")
	}
}

// Who may /send where, and who may hand files to which brain.
func TestScriptTokenScopes(t *testing.T) {
	const otherNumber = "15559876543@s.whatsapp.net"
	tb := tokenBridge(t, "")
	// The tokens of runs in progress, as brainRunEnv makes them.
	a, revokeA := tb.runTokens.mint("a")
	defer revokeA()
	b, revokeB := tb.runTokens.mint("b")
	defer revokeB()
	gone, revokeGone := tb.runTokens.mint("gone") // a brain removed since
	defer revokeGone()
	over, revokeOver := tb.runTokens.mint("a")
	revokeOver() // a run of a that is over
	scripts := tb.scriptAuth.scripts.Value()
	send := func(jid string) string { return `{"jid":"` + jid + `","text":"hello"}` }
	cases := []struct {
		name, path, body, token string
		want                    int
	}{
		{"brain a to your DM", "/send", send(tokenTestDM), a, 200},
		{"brain a to its group", "/send", send(tokenTestGroupA), a, 200},
		{"brain a to b's group", "/send", send(tokenTestGroupB), a, 403},
		{"brain a to a number not allowed", "/send", send(otherNumber), a, 403},
		{"brain b to its group", "/send", send(tokenTestGroupB), b, 200},
		{"brain b to your DM", "/send", send(tokenTestDM), b, 403},
		{"brain a, the fallback, to a group no brain claims", "/send", send(tokenTestGroupC), a, 200},
		{"brain b to a group no brain claims", "/send", send(tokenTestGroupC), b, 403},
		{"scripts to b's group", "/send", send(tokenTestGroupB), scripts, 200},
		{"scripts to any number", "/send", send(otherNumber), scripts, 200},
		{"no token to an allowed DM", "/send", send(tokenTestDM), "", 200},
		{"no token to an allowed group", "/send", send(tokenTestGroupB), "", 200},
		{"no token to a number not allowed", "/send", send(otherNumber), "", 403},
		{"no token to a LID", "/send", send("15551234567@lid"), "", 403},
		{"a made-up token", "/send", send(tokenTestDM), "not-a-token", 401},
		{"a removed brain's token", "/send", send(tokenTestDM), gone, 401},
		{"the token of a run that is over", "/send", send(tokenTestDM), over, 401},
		{"brain a into itself", "/ingest", `{"brain":"a","text":"x"}`, a, 202},
		{"brain a, naming no brain", "/ingest", `{"text":"x"}`, a, 202},
		{"brain a into b", "/ingest", `{"brain":"b","text":"x"}`, a, 403},
		{"scripts into b", "/ingest", `{"brain":"b","text":"x"}`, scripts, 202},
		{"no token into a", "/ingest", `{"brain":"a","text":"x"}`, "", 401},
		{"a made-up token into a", "/ingest", `{"brain":"a","text":"x"}`, "not-a-token", 401},
		{"the token of a run that is over, into a", "/ingest", `{"brain":"a","text":"x"}`, over, 401},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if w := tb.script("POST", c.path, c.body, c.token); w.Code != c.want {
				t.Fatalf("%d %s, want %d", w.Code, w.Body.String(), c.want)
			}
		})
	}

	off := tokenBridge(t, `, "script_api": {"tokenless_send": false}`)
	if w := off.script("POST", "/send", send(tokenTestDM), ""); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "token is required") {
		t.Errorf("no token with tokenless_send off: %d %s", w.Code, w.Body.String())
	}
	if w := off.script("POST", "/send", send(tokenTestDM), off.scriptAuth.scripts.Value()); w.Code != 200 {
		t.Errorf("scripts token with tokenless_send off: %d %s", w.Code, w.Body.String())
	}
}

// The Authorization header names the Bearer scheme in any case, as RFC 7235
// allows; anything else is a bad token, not an anonymous call.
func TestAuthorizeReadsTheBearerScheme(t *testing.T) {
	var runs runTokens
	run, revoke := runs.mint("a")
	defer revoke()
	auth, err := newScriptAuth(t.TempDir(), runs.brainOf, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	scripts := auth.scripts.Value()
	cases := []struct {
		name, header string
		want         principalKind
		bad          bool
	}{
		{"none", "", callerAnonymous, false},
		{"Bearer", "Bearer " + scripts, callerScript, false},
		{"lower case", "bearer " + scripts, callerScript, false},
		{"upper case, extra spaces", "BEARER   " + scripts + " ", callerScript, false},
		{"a brain's run's", "Bearer " + run, callerBrain, false},
		{"another scheme", "Basic " + scripts, 0, true},
		{"no scheme", scripts, 0, true},
		{"no token", "Bearer ", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/send", nil)
			if c.header != "" {
				r.Header.Set("Authorization", c.header)
			}
			who, err := auth.authorize(r)
			if c.bad {
				if !errors.Is(err, errBadToken) {
					t.Fatalf("authorize = %+v, %v; want errBadToken", who, err)
				}
				return
			}
			if err != nil || who.kind != c.want {
				t.Fatalf("authorize = %+v, %v; want kind %d", who, err, c.want)
			}
		})
	}
}

// The scripts token is a file in the data dir, made once and kept.
func TestScriptsTokenFile(t *testing.T) {
	tb := newTestBridge(t, "")
	path := filepath.Join(tb.cfgs.Get().DataDir, "scripts.token")
	data, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(data)) != tb.scriptAuth.scripts.Value() {
		t.Fatalf("scripts.token = %q, %v", data, err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	if tb.scriptAuth.scripts.Value() == tb.token.Value() {
		t.Error("the scripts token is the dashboard token")
	}
	// The dashboard API does not take it.
	if w := tb.do("GET", "/api/status", nil, func(r *http.Request) { r.Header.Set("X-Bridge-Token", tb.scriptAuth.scripts.Value()) }); w.Code != 401 {
		t.Errorf("dashboard API with the scripts token: %d", w.Code)
	}
}

// What came in through /send is marked in history, and the brain does not
// read a line a script or another brain sent as its own words.
func TestSendHistoryShowsWhoSentIt(t *testing.T) {
	tb := tokenBridge(t, "")
	a, revoke := tb.runTokens.mint("a")
	defer revoke()
	for _, c := range []struct{ text, token string }{
		{"from a script", tb.scriptAuth.scripts.Value()},
		{"from nobody", ""},
		{"from a itself", a},
	} {
		if w := tb.script("POST", "/send", `{"jid":"`+tokenTestDM+`","text":"`+c.text+`"}`, c.token); w.Code != 200 {
			t.Fatalf("%s: %d %s", c.text, w.Code, w.Body.String())
		}
	}
	data, _ := os.ReadFile(filepath.Join(tb.home, "a", "conversation-history.jsonl"))
	var got []historyEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e historyEntry
		_ = json.Unmarshal([]byte(line), &e)
		got = append(got, e)
	}
	want := []struct{ from, via string }{{"script", "script"}, {"script", "api"}, {"a", "brain"}}
	if len(got) != len(want) {
		t.Fatalf("history = %+v", got)
	}
	for i, w := range want {
		if got[i].From != w.from || got[i].Via != w.via || got[i].Type != "out" {
			t.Errorf("line %d = %+v, want from %q via %q", i, got[i], w.from, w.via)
		}
	}
	br := tb.brainMgr.Get("a")
	preface := br.buildPrompt(br.settings.Load(), Inbound{JID: tokenTestDM, Sender: "15551234567", Text: "next"})
	for _, line := range []string{
		`"from":"Script (via /send)","text":"from a script"`,
		`"from":"Script without a token (via /send)","text":"from nobody"`,
		`"from":"You (a, via /send)","text":"from a itself"`,
	} {
		if !strings.Contains(preface, line) {
			t.Errorf("preface lacks %q:\n%s", line, preface)
		}
	}
	if strings.Contains(preface, `"from":"You (a)","text":"from a script"`) || strings.Contains(preface, `"from":"You (a)","text":"from nobody"`) {
		t.Errorf("preface shows a script's line as the brain's own:\n%s", preface)
	}
}

func TestOutboundSender(t *testing.T) {
	cases := []struct {
		from, via, want string
	}{
		{"a", "", "You (a)"},
		{"a", "brain", "You (a, via /send)"},
		{"b", "brain", "Brain b (via /send)"},
		{"script", "script", "Script (via /send)"},
		{"script", "api", "Script without a token (via /send)"},
	}
	for _, c := range cases {
		if got := outboundSender("a", historyEntry{From: c.from, Via: c.via, Type: "out"}); got != c.want {
			t.Errorf("from %q via %q: %q, want %q", c.from, c.via, got, c.want)
		}
	}
}

// The OTP guard counts a code request a brain sent itself or that went out
// in its chat through /send, but not another brain's own replies.
func TestOTPGuardCountsSendLines(t *testing.T) {
	line := func(from, via string) string {
		data, _ := json.Marshal(historyEntry{From: from, Via: via, Type: "out", Text: "your OTP please"})
		return string(data)
	}
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"its own reply", []string{line("a", "")}, true},
		{"a script's request", []string{line("script", "script")}, true},
		{"a request without a token", []string{line("script", "api")}, true},
		{"its own /send", []string{line("a", "brain")}, true},
		{"another brain's reply", []string{line("b", "")}, false},
	}
	for _, c := range cases {
		if got := recentOutboundLooksLikeOTPRequest(c.lines, "a"); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
