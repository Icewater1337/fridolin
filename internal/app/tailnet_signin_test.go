package app

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/tailnet"
)

// A one-time sign-in link works at the core's tailnet address: a browser
// on another device of the tailnet opens the link from !dashboard (or
// `fridolin open --print --tailnet`), trades its code for a session at
// the tailnet host, over plain HTTP on 80 (the tailnet issues no
// certificates here), and the dashboard API then answers it without the
// token. The code works once.
func TestTailnetSignInWithALink(t *testing.T) {
	t.Parallel()
	fake := &tailnet.Fake{
		Status: tailnet.Status{DNSName: "fridolin-core.example.ts.net", IPs: []string{"100.64.0.1"}, MagicDNS: true},
		Who:    tailnet.Identity{LoginName: "you@example.com", Node: "pc"},
	}
	c, err := newRunningCore(t, `, "core": {"tailnet": {"enabled": true}}`, Options{Tailnet: tailnet.FakeFactory(fake), TailnetAuthKey: "tskey-test"})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the tailnet listener", func() bool { st := c.tailnetStatus(); return st != nil && st.State == "up" && st.URL != "" })
	addr := fake.Addr(":80")
	const host = "fridolin-core.example.ts.net"
	do := func(method, path, body string, hdr map[string]string, cookie *http.Cookie) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(data)
	}
	// What the dashboard page sends, at the tailnet host.
	page := map[string]string{"Origin": "http://" + host, "Sec-Fetch-Site": "same-origin", "X-Bridge-Dashboard": "1", "Content-Type": "application/json"}
	signIn := func(code string) (*http.Response, string) {
		return do("POST", "/api/login", `{"code":"`+code+`"}`, page, nil)
	}

	// !dashboard in the owner's DM.
	reply := c.ownerCommand(brain.Inbound{Inbound: chat.Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: "!dashboard"}}, "!dashboard", nil)
	prefix := "http://" + host + "/#login="
	i := strings.Index(reply, prefix)
	if i < 0 {
		t.Fatalf("!dashboard has no tailnet link:\n%s", reply)
	}
	code := strings.Fields(reply[i+len(prefix):])[0]
	if !strings.Contains(reply, "http://127.0.0.1:8765/#login="+code) {
		t.Errorf("!dashboard has no link on the core machine:\n%s", reply)
	}

	resp, body := signIn(code)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signing in on the tailnet: %d %s", resp.StatusCode, body)
	}
	var cookie *http.Cookie
	for _, k := range resp.Cookies() {
		if k.Name == "fridolin_session_8765" {
			cookie = k
		}
	}
	var signed struct{ Binding string }
	if cookie == nil || json.Unmarshal([]byte(body), &signed) != nil || signed.Binding == "" {
		t.Fatalf("no session: %v %s", resp.Cookies(), body)
	}
	// Plain HTTP inside WireGuard: a Secure cookie would not be kept.
	if !cookie.HttpOnly || cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie %+v, want HttpOnly, SameSite=Strict, not Secure over http", cookie)
	}
	if resp, _ := do("GET", "/api/status", "", map[string]string{httpapi.SessionHeader: signed.Binding}, cookie); resp.StatusCode != http.StatusOK {
		t.Errorf("the API with the session on the tailnet: %d", resp.StatusCode)
	}
	if resp, _ := signIn(code); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the link a second time: %d", resp.StatusCode)
	}
	// Another site cannot sign a browser in there.
	evil := map[string]string{"Origin": "http://evil.example", "X-Bridge-Dashboard": "1", "Content-Type": "application/json"}
	if resp, _ := do("POST", "/api/login", `{"code":"x"}`, evil, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a sign-in from another site: %d", resp.StatusCode)
	}

	// `fridolin open --print --tailnet` asks for the same with the token.
	tok := strings.TrimSpace(readFile(t, filepath.Join(c.home, "data", "dashboard.token")))
	resp, body = do("POST", "/api/login/code?tailnet=1", "", map[string]string{"X-Bridge-Token": tok, "X-Bridge-Dashboard": "1"}, nil)
	var lc httpapi.LoginCodeResponse
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &lc) != nil || lc.URL != prefix+lc.Code {
		t.Fatalf("login code for the tailnet: %d %s", resp.StatusCode, body)
	}
	if resp, body := signIn(lc.Code); resp.StatusCode != http.StatusOK {
		t.Errorf("signing in with the CLI's link: %d %s", resp.StatusCode, body)
	}
}
