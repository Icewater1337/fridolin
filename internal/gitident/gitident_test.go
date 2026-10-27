package gitident

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A fake token, never a real one.
const testToken = "test-gitlab-token-000000"

func ptr(s string) *string { return &s }

// The connection is kept 0600 in a 0700 folder; a blank token keeps the
// stored one, and what is wrong is refused before anything is written.
func TestSetGitLab(t *testing.T) {
	t.Parallel()
	s := Store{Dir: filepath.Join(t.TempDir(), "core", "local-secrets")}
	if _, ok, err := s.GitLab(); ok || err != nil {
		t.Fatalf("nothing set: %v %v", ok, err)
	}
	for _, u := range []Update{
		{Token: testToken},
		{BaseURL: ptr("https://gitlab.example.com")},
		{BaseURL: ptr("http://gitlab.example.com"), Token: testToken},
		{BaseURL: ptr("https://you:pw@example.com"), Token: testToken},
		{BaseURL: ptr("https://gitlab.example.com?x=1"), Token: testToken},
		{BaseURL: ptr("https://gitlab_example.com"), Token: testToken},
		{BaseURL: ptr("https://gitlab.example.com"), Token: "short"},
		{BaseURL: ptr("https://gitlab.example.com"), Token: "has a space in it"},
		{BaseURL: ptr("https://gitlab.example.com"), Token: testToken, Name: ptr("Jane")},
		{BaseURL: ptr("https://gitlab.example.com"), Token: testToken, Name: ptr("Jane"), Email: ptr("not an address")},
		{BaseURL: ptr("https://gitlab.example.com"), Token: testToken, Name: ptr("Ja<ne>"), Email: ptr("jane@example.com")},
	} {
		if _, err := s.SetGitLab(u); err == nil {
			t.Errorf("%+v was taken", u)
		} else if e, ok := err.(*Error); !ok || e.HTTPStatus() != http.StatusBadRequest {
			t.Errorf("%+v: %v, want a 400", u, err)
		}
	}
	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Fatalf("a refusal wrote the folder: %v", err)
	}
	c, err := s.SetGitLab(Update{BaseURL: ptr(" https://GitLab.Example.com:8443/gitlab/ "), Token: testToken, Name: ptr(" Jane Doe "), Email: ptr("jane@example.com")})
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "https://gitlab.example.com:8443/gitlab" || c.Host() != "gitlab.example.com:8443" || c.Name != "Jane Doe" {
		t.Errorf("saved %+v, host %q", c, c.Host())
	}
	for p, want := range map[string]os.FileMode{s.Dir: 0o700, filepath.Join(s.Dir, GitLabFile): 0o600} {
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() != want {
			t.Errorf("%s: %v %v, want %o", p, st, err, want)
		}
	}
	// A blank token keeps the token; the address and identity change.
	c, err = s.SetGitLab(Update{BaseURL: ptr("https://gitlab.example.com"), Name: ptr(""), Email: ptr("")})
	if err != nil || c.Token != testToken || c.BaseURL != "https://gitlab.example.com" || c.Name != "" {
		t.Errorf("blank token: %+v %v", c, err)
	}
	if got, ok, err := s.GitLab(); !ok || err != nil || got != c {
		t.Errorf("read back %+v %v %v", got, ok, err)
	}
	if err := s.RemoveGitLab(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GitLab(); ok {
		t.Error("removed, still set")
	}
	if err := s.RemoveGitLab(); err != nil {
		t.Errorf("removing none: %v", err)
	}
}

// GitHub's commit name and email: both or neither; neither removes them.
func TestGitHubCommit(t *testing.T) {
	t.Parallel()
	s := Store{Dir: t.TempDir()}
	if c, err := s.GitHubCommit(); err != nil || c != (Commit{}) {
		t.Fatalf("none: %+v %v", c, err)
	}
	if _, err := s.SetGitHubCommit("Jane", ""); err == nil {
		t.Error("a name without an email was taken")
	}
	if _, err := s.SetGitHubCommit("Jane Doe", "jane@example.com"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.GitHubCommit(); c != (Commit{Name: "Jane Doe", Email: "jane@example.com"}) {
		t.Errorf("read back %+v", c)
	}
	if _, err := s.SetGitHubCommit(" ", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, GitHubFile)); !os.IsNotExist(err) {
		t.Errorf("neither keeps the file: %v", err)
	}
}

func TestCheckHost(t *testing.T) {
	t.Parallel()
	for _, h := range []string{"github.com", "gitlab.example.com:8443", "127.0.0.1", "[::1]:443", "a-b.c"} {
		if err := CheckHost(h); err != nil {
			t.Errorf("%s: %v", h, err)
		}
	}
	for _, h := range []string{"", "-a.com", "a_b.com", "a.com:", "a.com:99999", "a.com:0", "a com", "a.com/x"} {
		if CheckHost(h) == nil {
			t.Errorf("%q was taken", h)
		}
	}
}

// fakeGitLab answers /api/v4/user for testToken, and records the tokens
// it was sent.
type fakeGitLab struct {
	*httptest.Server
	mu     sync.Mutex
	tokens []string
}

func newFakeGitLab(t *testing.T, h http.HandlerFunc) *fakeGitLab {
	f := &fakeGitLab{}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.tokens = append(f.tokens, r.Header.Get("PRIVATE-TOKEN"))
		f.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

// Check asks GitLab who the token is, says what went wrong in words, and
// never follows a redirect, so the token stays with GitLab.
func TestCheck(t *testing.T) {
	t.Parallel()
	elsewhere := newFakeGitLab(t, func(w http.ResponseWriter, r *http.Request) {})
	gl := newFakeGitLab(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/moved/api/v4/user":
			http.Redirect(w, r, elsewhere.URL+"/api/v4/user", http.StatusFound)
		case r.URL.Path == "/plain/api/v4/user":
			_, _ = w.Write([]byte("<html>not gitlab</html>"))
		case r.URL.Path != "/api/v4/user":
			http.NotFound(w, r)
		case r.Header.Get("PRIVATE-TOKEN") == testToken:
			_, _ = w.Write([]byte(`{"id": 1, "username": "jane"}`))
		case r.Header.Get("PRIVATE-TOKEN") == "test-forbidden-000":
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	ctx := context.Background()
	c := Connection{BaseURL: gl.URL, Token: testToken}
	if u, err := Check(ctx, gl.Client(), c); err != nil || u != "jane" {
		t.Fatalf("check: %q %v", u, err)
	}
	for _, tc := range []struct {
		c    Connection
		want string
	}{
		{Connection{BaseURL: gl.URL, Token: "test-wrong-token-000"}, "(401)"},
		{Connection{BaseURL: gl.URL, Token: "test-forbidden-000"}, "read_user or api scope"},
		{Connection{BaseURL: gl.URL + "/moved", Token: testToken}, "redirect (302)"},
		{Connection{BaseURL: gl.URL + "/plain", Token: testToken}, "not as GitLab's API does"},
		{Connection{BaseURL: gl.URL + "/nope", Token: testToken}, "answered 404"},
		{Connection{}, "no GitLab connection"},
	} {
		_, err := Check(ctx, gl.Client(), tc.c)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.c.BaseURL, err, tc.want)
		}
		if err != nil && tc.c.Token != "" && strings.Contains(err.Error(), tc.c.Token) {
			t.Errorf("the error shows the token: %v", err)
		}
	}
	if len(elsewhere.tokens) != 0 {
		t.Errorf("the redirect was followed with %v", elsewhere.tokens)
	}
	// Without the fake's certificate the TLS handshake fails, and the
	// answer names the host, not the token.
	if _, err := Check(ctx, nil, c); err == nil || strings.Contains(err.Error(), testToken) || !strings.Contains(err.Error(), "does not answer") {
		t.Errorf("an untrusted certificate: %v", err)
	}
}
