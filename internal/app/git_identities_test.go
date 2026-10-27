package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/ghapp/ghapptest"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// Fake GitLab tokens, never real ones.
const (
	testGitLabToken  = "test-gitlab-token-0000aa11"
	testGitLabToken2 = "test-gitlab-token-0000bb22"
)

// fakeGitLab is GitLab's API on a TLS test server: /api/v4/user knows
// testGitLabToken as jane.
type fakeGitLab struct {
	*httptest.Server
	mu     sync.Mutex
	tokens []string
}

// useFakeGitLab points the GitLab check of the bridges t makes at a fake.
func useFakeGitLab(t *testing.T) *fakeGitLab {
	t.Helper()
	f := &fakeGitLab{}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.tokens = append(f.tokens, r.Header.Get("PRIVATE-TOKEN"))
		f.mu.Unlock()
		if r.URL.Path != "/api/v4/user" || r.Header.Get("PRIVATE-TOKEN") != testGitLabToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id": 7, "username": "jane"}`))
	}))
	t.Cleanup(f.Close)
	optionsOf(t).hooks.gitlab = f.Client()
	return f
}

// noTokens fails when a GitLab or GitHub token shows in data.
func noTokens(t *testing.T, what string, data []byte, tokens ...string) {
	t.Helper()
	for _, k := range append([]string{testGitLabToken, testGitLabToken2}, tokens...) {
		if k != "" && bytes.Contains(data, []byte(k)) {
			t.Errorf("%s shows a token: %s", what, data)
		}
	}
}

// TestGitLabConnection: the Integrations page sets the GitLab connection,
// whose token never comes back and is kept on this machine only, checks
// it with GitLab, sets GitHub's commit name, and removes it.
func TestGitLabConnection(t *testing.T) {
	t.Parallel()
	gl := useFakeGitLab(t)
	h := newHarness(t, "")
	for _, c := range []struct {
		body any
		want int
	}{
		{map[string]any{"token": testGitLabToken}, 400},
		{map[string]any{"base_url": "http://gitlab.example.com", "token": testGitLabToken}, 400},
		{map[string]any{"base_url": gl.URL, "token": "short"}, 400},
		{map[string]any{"base_url": gl.URL, "token": testGitLabToken, "name": "Jane"}, 400},
		{map[string]any{"base_url": gl.URL, "token": testGitLabToken, "secret": "x"}, 400},
	} {
		if w := h.Do("PUT", "/api/gitlab", c.body); w.Code != c.want {
			t.Errorf("PUT %v: %d %s", c.body, w.Code, w.Body.String())
		}
	}
	if w := h.Do("POST", "/api/gitlab/check", nil); !strings.Contains(w.Body.String(), "no GitLab connection") {
		t.Errorf("check without one: %d %s", w.Code, w.Body.String())
	}

	w := h.Do("PUT", "/api/gitlab", map[string]any{"base_url": gl.URL + "/", "token": testGitLabToken, "name": "Jane Doe", "email": "jane@example.com"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"configured":true`) || !strings.Contains(w.Body.String(), `"token_hint":"…aa11"`) || !strings.Contains(w.Body.String(), `"base_url":"`+gl.URL+`"`) {
		t.Fatalf("saving: %d %s", w.Code, w.Body.String())
	}
	noTokens(t, "the answer", w.Body.Bytes())
	file := filepath.Join(h.DataDir(), "core", "local-secrets", "gitlab.json")
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the connection's file: %v %v", st, err)
	}
	// A blank token keeps it.
	if w := h.Do("PUT", "/api/gitlab", map[string]any{"token": "", "name": "Jane Q. Doe"}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"token_hint":"…aa11"`) || !strings.Contains(w.Body.String(), `"name":"Jane Q. Doe"`) {
		t.Errorf("a blank token: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("POST", "/api/gitlab/check", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) || !strings.Contains(w.Body.String(), "knows the token as jane") {
		t.Errorf("check: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("PUT", "/api/gitlab", map[string]any{"token": testGitLabToken2}); w.Code != http.StatusOK {
		t.Fatalf("replacing the token: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("POST", "/api/gitlab/check", nil); !strings.Contains(w.Body.String(), `"ok":false`) || !strings.Contains(w.Body.String(), "(401)") {
		t.Errorf("a wrong token: %d %s", w.Code, w.Body.String())
	}
	gl.mu.Lock()
	sent := strings.Join(gl.tokens, ",")
	gl.mu.Unlock()
	if sent != testGitLabToken+","+testGitLabToken2 {
		t.Errorf("the checks sent %q", sent)
	}

	// GitHub's commit name and email: both or neither.
	if w := h.Do("PUT", "/api/github/identity", map[string]string{"name": "Jane"}); w.Code != http.StatusBadRequest {
		t.Errorf("a name without an email: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("PUT", "/api/github/identity", map[string]string{"name": "Jane Doe", "email": "you@example.com"}); w.Code != http.StatusOK {
		t.Errorf("the GitHub identity: %d %s", w.Code, w.Body.String())
	}
	v := h.Do("GET", "/api/git-identities", nil)
	if !strings.Contains(v.Body.String(), `"email":"you@example.com"`) || !strings.Contains(v.Body.String(), `"set":true`) {
		t.Errorf("the identities: %s", v.Body.String())
	}

	// Nothing shows the token: answers, config.json, the audit, the log;
	// and the connections' folder, which exports carry, never holds it.
	for _, p := range []string{"/api/gitlab", "/api/git-identities", "/api/config", "/api/logs", "/api/audit", "/api/connections"} {
		noTokens(t, "GET "+p, h.Do("GET", p, nil).Body.Bytes())
	}
	if data, err := os.ReadFile(h.cfg); err == nil {
		noTokens(t, "config.json", data)
	}
	_ = filepath.WalkDir(filepath.Join(h.DataDir(), "connections"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			data, _ := os.ReadFile(p)
			noTokens(t, p, data)
		}
		return nil
	})
	var lines []string
	for _, l := range h.auditOf("connection_key") {
		lines = append(lines, l["detail"].(string))
	}
	if strings.Join(lines, "|") != "gitlab: token set; address "+gl.URL+"; commit name and email saved|gitlab: commit name and email saved|gitlab: token replaced|github: commit name and email saved" {
		t.Errorf("audit %q", lines)
	}

	if w := h.Do("DELETE", "/api/gitlab", nil); w.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("the file stays: %v", err)
	}
	if w := h.Do("GET", "/api/gitlab", nil); !strings.Contains(w.Body.String(), `"configured":false`) {
		t.Errorf("after removing: %s", w.Body.String())
	}
}

// TestGitIdentitiesReachFrIDE: the FrIDE of every node with fride gets the
// GitHub app's installation token and the GitLab connection, or what
// nodes.git_identities names for it; a removed connection goes from
// FrIDE, and a node that asks gets a new GitHub token before the one it
// has runs out. No token shows in the dashboard, the log or the audit.
func TestGitIdentitiesReachFrIDE(t *testing.T) {
	t.Parallel()
	useFakeGitLab(t)
	laptopFride, miniFride := testutil.StartFakeFride(t, "api"), testutil.StartFakeFride(t, "api")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766", "git_identities": {"mini": ["github"]}}}`)
	fake := useFakeGitHub(t, h.b.github)
	fake.SetPermissions(map[string]string{"contents": "write", "metadata": "read", "pull_requests": "write"})
	creds, err := h.b.github.client.Convert(context.Background(), ghapptest.Code)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.b.github.secrets.save(creds); err != nil {
		t.Fatal(err)
	}
	if w := h.Do("PUT", "/api/gitlab", map[string]any{"base_url": "https://gitlab.example.com", "token": testGitLabToken, "name": "Jane Doe", "email": "jane@example.com"}); w.Code != http.StatusOK {
		t.Fatalf("the GitLab connection: %d %s", w.Code, w.Body.String())
	}
	addr := h.StartNodes()
	laptop, _ := h.fakeFrideNode("laptop", addr, laptopFride)
	mini, _ := h.fakeFrideNode("mini", addr, miniFride)

	h.WaitFor("the laptop's FrIDE to have both", func() bool {
		ids := laptopFride.GitIdentities()
		return ids["github"].Token != "" && ids["gitlab"].Token == testGitLabToken
	})
	h.WaitFor("the mini's FrIDE to have GitHub", func() bool { return miniFride.GitIdentities()["github"].Token != "" })
	ghTok := laptopFride.GitIdentities()["github"].Token
	if m := fake.LastMinted(); m.Token != ghTok || m.Repos != nil || m.Permissions != nil {
		t.Errorf("FrIDE got %q, the fake minted %+v", ghTok, m)
	}
	if got := laptopFride.GitIdentities()["gitlab"]; got.Host != "gitlab.example.com" || got.Kind != "gitlab" || got.Email != "jane@example.com" {
		t.Errorf("FrIDE's GitLab identity: %+v", got)
	}
	if _, ok := miniFride.GitIdentities()["gitlab"]; ok {
		t.Error("the mini got GitLab, which nodes.git_identities keeps from it")
	}
	h.b.gitIDs.wait()
	var view gitIdentitiesView
	w := h.Do("GET", "/api/git-identities", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || len(view.Nodes) != 2 {
		t.Fatalf("the view: %s %v", w.Body.String(), err)
	}
	for _, n := range view.Nodes {
		want := map[string]string{laptop: "github,gitlab", mini: "github"}[n.ID]
		if strings.Join(n.Got, ",") != want || !n.Supported || n.Error != "" {
			t.Errorf("node %s: %+v, want %s", n.Name, n, want)
		}
	}

	// GitLab goes, and with it from the laptop's FrIDE.
	if w := h.Do("DELETE", "/api/gitlab", nil); w.Code != http.StatusNoContent {
		t.Fatalf("remove: %d", w.Code)
	}
	h.WaitFor("GitLab to go from the laptop's FrIDE", func() bool {
		_, ok := laptopFride.GitIdentities()["gitlab"]
		return !ok
	})

	// The mini asks for a new GitHub token while its own runs out soon.
	h.b.github.mu.Lock()
	h.b.github.frideTok.ExpiresAt = time.Now().Add(10 * time.Minute)
	h.b.github.mu.Unlock()
	data, _ := json.Marshal(nodeproto.FrideIdentitiesAsk{Reason: "expiring"})
	h.b.gitIDs.onEvent(mini, nodeproto.Event{Kind: nodeproto.EventFrideIdentities, Data: data})
	h.WaitFor("a new GitHub token on the mini", func() bool {
		tok := miniFride.GitIdentities()["github"].Token
		return tok != "" && tok != ghTok
	})

	for _, p := range []string{"/api/git-identities", "/api/nodes", "/api/logs", "/api/audit"} {
		noTokens(t, "GET "+p, h.Do("GET", p, nil).Body.Bytes(), ghTok, fake.LastMinted().Token)
	}
}
