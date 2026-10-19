package google

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

type googleRoundTripFunc func(*http.Request) (*http.Response, error)

func (f googleRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// This service-level flow can run in parallel because its endpoints, HTTP
// client, pending state and paths belong to one instance. The dashboard flow
// still checks command composition and its HTTP guards with the bridge fixture.
func TestCalendarSignInFlow(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"first instance", "second instance"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv, got := fakeGoogle(t)
			root := t.TempDir()
			creds := filepath.Join(root, "before-sign-in")
			current := manifest.Manifest{ID: "a"}
			configuredCalls := 0
			options := googleTestOptions(Options{
				ClientPath: filepath.Join(root, "client.json"), RedirectURI: "http://127.0.0.1:8765/",
				CredsDir:   func() string { return creds },
				Configured: func(id string) bool { configuredCalls++; return id == current.ID },
				UpdateManifest: func(id string, update func(*manifest.Manifest) error) error {
					if id != current.ID {
						return fmt.Errorf("unknown brain %s", id)
					}
					return update(&current)
				},
			}, srv)
			options.Endpoints.Auth = srv.URL + "/authorize"
			var requests atomic.Int32
			transport := options.HTTP.Transport
			if transport == nil {
				transport = http.DefaultTransport
			}
			options.HTTP.Transport = googleRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests.Add(1)
				return transport.RoundTrip(r)
			})
			g := New(options)
			if _, err := g.StartSignIn("a"); err == nil {
				t.Fatal("sign-in without client succeeded")
			}
			// The file's URL is deliberately different from the injected token endpoint.
			if _, err := g.SetClient([]byte(fmt.Sprintf(testClientJSON, "https://ignored.example.com"))); err != nil {
				t.Fatal(err)
			}
			auth, err := g.StartSignIn("a")
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(auth)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			if !strings.HasPrefix(auth, options.Endpoints.Auth+"?") || q.Get("redirect_uri") != options.RedirectURI || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" {
				t.Fatalf("authorization parameters: %s", auth)
			}
			if configuredCalls != 0 {
				t.Fatal("configured brains were read before OAuth completion")
			}
			// Credential destination is read at completion, not frozen with the client.
			creds = filepath.Join(root, "at-completion")
			callback := func(state string) (string, error) {
				return g.Callback(t.Context(), url.Values{"state": {state}, "code": {"good-code"}})
			}
			if redirect, err := callback("forged"); redirect != "" || err == nil {
				t.Fatalf("forged state: redirect=%q error=%v", redirect, err)
			}
			if redirect, err := callback(q.Get("state")); redirect != "/#/brains/a" || err != nil {
				t.Fatalf("callback: redirect=%q error=%v", redirect, err)
			}
			if configuredCalls != 1 || current.Calendar == nil || current.Calendar.Account != "you@example.com" {
				t.Fatalf("configured calls=%d calendar=%+v", configuredCalls, current.Calendar)
			}
			challenge := sha256.Sum256([]byte((*got)["code_verifier"]))
			if (*got)["code"] != "good-code" || base64.RawURLEncoding.EncodeToString(challenge[:]) != q.Get("code_challenge") {
				t.Fatal("code exchange did not retain its PKCE verifier")
			}
			if _, err := os.Stat(TokenFile(creds, "a")); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(TokenFile(filepath.Join(root, "before-sign-in"), "a")); !os.IsNotExist(err) {
				t.Fatalf("token was written to the startup credentials path: %v", err)
			}
			if redirect, err := callback(q.Get("state")); redirect != "" || err == nil {
				t.Fatalf("replayed state: redirect=%q error=%v", redirect, err)
			}
			status, _ := json.Marshal(g.CalendarStatus(t.Context(), "a", current.Calendar, true))
			for _, secret := range []string{"TEST-CLIENT-SECRET", "ACCESS-1", "RT-1"} {
				if strings.Contains(string(status), secret) {
					t.Fatalf("calendar status leaked %s", secret)
				}
			}
			if revoked, _, err := g.Disconnect(t.Context(), "a", false); err != nil || !revoked {
				t.Fatalf("disconnect=%v %v", revoked, err)
			}
			if requests.Load() < 4 {
				t.Fatalf("injected HTTP client handled only %d requests", requests.Load())
			}
			if (*got)["revoked"] != "RT-1" || current.Calendar != nil {
				t.Fatal("disconnect did not revoke the token and clear the manifest")
			}
		})
	}
}
