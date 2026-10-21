package google

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

const testDriveScope = "https://www.googleapis.com/auth/drive.file"

// TestStartSignInFor: a sign-in for something other than a brain (the
// Drive backup's) asks for its own scopes with the client it is given,
// comes back through the same redirect, hands its token to done and sends
// the browser where it was told; a refusal, a missing scope or a missing
// refresh token reach done as errors, and a state is good once.
func TestStartSignInFor(t *testing.T) {
	cases := []struct {
		name   string
		answer string // the token endpoint's body; "" is a good answer with the scope
		query  url.Values
		want   string // in done's error; "" for a token
	}{
		{"granted", "", url.Values{"code": {"good"}}, ""},
		{"cancelled", "", url.Values{"error": {"access_denied"}}, "cancelled"},
		{"scope unticked", `{"access_token":"A","refresh_token":"R","expires_in":3600,"scope":"openid"}`, url.Values{"code": {"good"}}, "missing " + testDriveScope},
		{"no refresh token", `{"access_token":"A","expires_in":3600,"scope":"` + testDriveScope + `"}`, url.Values{"code": {"good"}}, "no refresh token"},
		{"exchange refused", `{"error":"invalid_grant","error_description":"Bad Request"}`, url.Values{"code": {"good"}}, "Bad Request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var form url.Values
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				form = r.PostForm
				body := tc.answer
				if body == "" {
					body = `{"access_token":"A","refresh_token":"R","expires_in":3600,"scope":"` + testDriveScope + `"}`
				}
				if strings.Contains(body, `"error"`) {
					w.WriteHeader(400)
				}
				fmt.Fprint(w, body)
			}))
			t.Cleanup(srv.Close)
			opts := googleTestOptions(Options{ClientPath: filepath.Join(t.TempDir(), "client.json"), RedirectURI: "http://127.0.0.1:8765/",
				CredsDir: t.TempDir, Configured: func(string) bool { return false }}, srv)
			opts.Endpoints.Auth = srv.URL + "/authorize"
			g := New(opts)
			if _, err := g.StartSignInFor(nil, []string{testDriveScope}, "/#/backup", func(*Token, error) {}); err == nil {
				t.Fatal("a sign-in without a client started")
			}
			var got *Token
			var gotErr error
			calls := 0
			c := &Client{Type: "web", ClientID: "drive-client", ClientSecret: "DRIVE-SECRET"}
			auth, err := g.StartSignInFor(c, []string{testDriveScope}, "/#/backup", func(tok *Token, err error) { calls++; got, gotErr = tok, err })
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(auth)
			q := u.Query()
			if q.Get("scope") != testDriveScope || q.Get("client_id") != "drive-client" || q.Get("redirect_uri") != "http://127.0.0.1:8765/" || q.Get("access_type") != "offline" {
				t.Fatalf("authorization URL %s", auth)
			}
			tc.query.Set("state", q.Get("state"))
			back, err := g.Callback(t.Context(), tc.query)
			if err != nil || back != "/#/backup" {
				t.Fatalf("callback = %q, %v", back, err)
			}
			if calls != 1 {
				t.Fatalf("done ran %d times", calls)
			}
			if tc.want == "" {
				if gotErr != nil || got == nil || got.RefreshToken != "R" || got.Token != "A" || got.ClientID != "drive-client" {
					t.Fatalf("done got %+v, %v", got, gotErr)
				}
				if form.Get("client_secret") != "DRIVE-SECRET" || form.Get("redirect_uri") != "http://127.0.0.1:8765/" {
					t.Errorf("exchange form %v", form)
				}
			} else if gotErr == nil || !strings.Contains(gotErr.Error(), tc.want) || got != nil {
				t.Fatalf("done got %+v, %v; want an error with %q", got, gotErr, tc.want)
			}
			if _, err := g.Callback(t.Context(), tc.query); err == nil || calls != 1 {
				t.Errorf("a replayed state = %v, done ran %d times", err, calls)
			}
		})
	}
}
