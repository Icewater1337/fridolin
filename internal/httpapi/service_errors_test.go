package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/google"
	"github.com/Icewater1337/fridolin/internal/plaud"
)

// Unused operations intentionally remain nil: these handlers must use only
// the named service operation, even when it reports a failure.
type backupConnectFake struct {
	BackupService
	err       error
	calls     int
	reconnect bool
}

func (f *backupConnectFake) Connect(reconnect bool) (string, error) {
	f.calls++
	f.reconnect = reconnect
	return "https://accounts.example.com/auth", f.err
}

// TestBackupConnectTakesReconnect: the body may be left out; one that
// says reconnect asks for the switch of a working link, and the answer
// carries Google's consent page.
func TestBackupConnectTakesReconnect(t *testing.T) {
	f := &backupConnectFake{}
	a := BackupAPI{Backup: func() BackupService { return f }}
	w := httptest.NewRecorder()
	a.Connect(w, httptest.NewRequest("POST", "/connect", strings.NewReader(`{"reconnect":true}`)))
	if w.Code != 202 || !f.reconnect || !strings.Contains(w.Body.String(), `"auth_url":"https://accounts.example.com/auth"`) {
		t.Fatalf("%d %s reconnect=%v", w.Code, w.Body.String(), f.reconnect)
	}
	w = httptest.NewRecorder()
	a.Connect(w, httptest.NewRequest("POST", "/connect", strings.NewReader(`{"reconnect":1}`)))
	if w.Code != 400 {
		t.Errorf("a bad body = %d", w.Code)
	}
}
func TestBackupConnectClassifiesServiceFailures(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{apperr.New(apperr.NotFound, "missing"), 404},
		{apperr.New(apperr.Invalid, "invalid"), 422},
		{fmt.Errorf("operation: %w", apperr.New(apperr.Busy, "busy")), 409},
		{apperr.New(apperr.TooLarge, "large"), 413},
		{apperr.New(apperr.BadJSON, "bad JSON"), 400},
		{apperr.New(apperr.Kind(255), "future kind"), 500},
		{nil, 202}, {apperr.New(apperr.Precondition, "missing tool"), 412}, {apperr.New(apperr.Conflict, "busy"), 409}, {apperr.New(apperr.Unavailable, "offline"), 503}, {errors.New("unexpected"), 500},
	} {
		f := &backupConnectFake{err: tc.err}
		a := BackupAPI{Backup: func() BackupService { return f }}
		w := httptest.NewRecorder()
		a.Connect(w, httptest.NewRequest("POST", "/connect", nil))
		if w.Code != tc.status || f.calls != 1 {
			t.Fatalf("%v: %d calls=%d", tc.err, w.Code, f.calls)
		}
	}
}

type googleClientFake struct {
	GoogleService
	client *google.Client
	err    error
}

func (f *googleClientFake) Client() *google.Client  { return f.client }
func (f *googleClientFake) Builtin() *google.Client { return nil }
func (f *googleClientFake) RedirectURI() string     { return "http://localhost/callback" }
func (f *googleClientFake) DeleteClient() error     { return f.err }
func TestGoogleClientHidesClientIdentifierAndPreservesEmptyType(t *testing.T) {
	f := &googleClientFake{client: &google.Client{ClientID: "prefix-private", Type: ""}}
	a := GoogleAPI{Google: func() GoogleService { return f }}
	w := httptest.NewRecorder()
	a.Client(w, httptest.NewRequest("GET", "/client", nil))
	if strings.Contains(w.Body.String(), "private") || !strings.Contains(w.Body.String(), `"client_id_hint":"prefix-…"`) || !strings.Contains(w.Body.String(), `"type":""`) {
		t.Fatal(w.Body.String())
	}
	f.err = errors.New("cannot persist removal")
	w = httptest.NewRecorder()
	a.DeleteClient(w, httptest.NewRequest("DELETE", "/client", nil))
	if w.Code != 500 {
		t.Fatal(w.Code)
	}
}

type plaudResendFake struct {
	PlaudService
	err error
	id  string
}

func (f *plaudResendFake) Resend(id string) error { f.id = id; return f.err }
func TestPlaudResendUsesRecordingAndClassifiesFailure(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{nil, 202}, {plaud.ErrUnknownRecording, 404}, {plaud.ErrAlreadyWaiting, 409}, {errors.New("write failed"), 500}} {
		f := &plaudResendFake{err: tc.err}
		a := PlaudAPI{Plaud: func() PlaudService { return f }}
		w := httptest.NewRecorder()
		a.Resend(w, httptest.NewRequest("POST", "/resend", strings.NewReader(`{"id":"recording"}`)))
		if w.Code != tc.status || f.id != "recording" {
			t.Fatalf("%v: %d id=%q", tc.err, w.Code, f.id)
		}
	}
}

func TestBuilderUsesTokenOwnerAndDoesNotRunUnauthorizedOperation(t *testing.T) {
	calls := 0
	a := BuilderAPI{BrainForToken: func(token string) string {
		if token == "allowed" {
			return "builder"
		}
		return ""
	}, Sync: func(_ context.Context, id string) (string, error) {
		calls++
		if id != "builder" {
			t.Fatal(id)
		}
		return "synced", nil
	}}
	for _, token := range []string{"denied", "allowed"} {
		r := httptest.NewRequest("POST", "/builder/sync", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if token == "denied" {
			if w.Code != 401 || calls != 0 {
				t.Fatalf("unauthorized: %d calls=%d", w.Code, calls)
			}
		} else if w.Code != 200 || calls != 1 || w.Body.String() != "{\"result\":\"synced\"}\n" {
			t.Fatalf("authorized: %d %s calls=%d", w.Code, w.Body, calls)
		}
	}
}
