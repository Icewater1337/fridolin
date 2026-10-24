package google

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

func TestParseGoogleClient(t *testing.T) {
	c, err := ParseClient([]byte(fmt.Sprintf(testClientJSON, "https://oauth2.googleapis.com")))
	if err != nil || c.Type != "installed" || c.ClientID == "" {
		t.Fatalf("parse = %+v, %v", c, err)
	}
	for _, bad := range []string{`{}`, `{"installed":{}}`, `not json`, `{"service_account":{"x":1}}`} {
		if _, err := ParseClient([]byte(bad)); err == nil {
			t.Errorf("%s should be rejected", bad)
		}
	}
}

// TestCalendarTokenBrain: the brain a token file is for is read back from
// the name TokenFile gives it, and from no other file there.
func TestCalendarTokenBrain(t *testing.T) {
	cases := []struct {
		name, id string
		ok       bool
	}{
		{filepath.Base(TokenFile("creds", "a")), "a", true},
		{filepath.Base(TokenFile("creds", "work-2")), "work-2", true},
		{"google_calendar_credentials_a.json", "", false}, // an older version's copy of the client
		{filepath.Base(TokenFile("creds", "")), "", false},
		{"google_calendar_token_a.json.bak", "", false},
		{"rclone.conf", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if id, ok := calendarTokenBrain(tc.name); id != tc.id || ok != tc.ok {
				t.Errorf("calendarTokenBrain = %q, %v; want %q, %v", id, ok, tc.id, tc.ok)
			}
		})
	}
}

func TestCalendarEnv(t *testing.T) {
	cases := []struct {
		name string
		cal  *manifest.Calendar
		want []string
	}{
		{"no calendars chosen", nil, []string{"BRIDGE_BRAIN_ID=a"}},
		{"none ticked", &manifest.Calendar{Account: "you@example.com"}, []string{"BRIDGE_BRAIN_ID=a"}},
		{"calendars chosen", &manifest.Calendar{Account: "you@example.com", Calendars: []string{"you@example.com", "team@example.com"}, Default: "team@example.com"},
			[]string{"BRIDGE_BRAIN_ID=a", "BRIDGE_CALENDARS=you@example.com,team@example.com", "BRIDGE_CALENDAR_DEFAULT=team@example.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CalendarEnv("a", manifest.Manifest{ID: "a", Calendar: tc.cal}, "/Users/you/creds"); !slices.Equal(got, tc.want) {
				t.Errorf("CalendarEnv = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCalendarTokenGoesToCredentialsDir(t *testing.T) {
	root := t.TempDir()
	g := New(Options{ClientPath: filepath.Join(root, "client.json"), CredsDir: func() string { return filepath.Join(root, "creds") }})
	if got, want := g.TokenPath("x"), filepath.Join(root, "creds", "google_calendar_token_x.json"); got != want {
		t.Errorf("token path = %s, want %s", got, want)
	}

	// An existing (possibly write-protected) credentials file is never replaced.
	_ = os.MkdirAll(filepath.Join(root, "creds"), 0o700)
	keep := filepath.Join(root, "creds", "google_calendar_credentials_x.json")
	_ = os.WriteFile(keep, []byte("original"), 0o600)
	_, _ = g.SetClient([]byte(fmt.Sprintf(testClientJSON, "https://oauth2.googleapis.com")))
	if err := g.saveToken("x", &Token{Token: "a", RefreshToken: "b"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(keep); string(data) != "original" {
		t.Error("an existing credentials file was overwritten")
	}
}

func TestCalendarCallProblems(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		err       error
		status    int
		reconnect bool
		want      string
	}{
		{"not connected", &fs.PathError{Op: "open", Path: "token.json", Err: fs.ErrNotExist}, 412, false, "not connected for this brain"},
		{"a token the bridge cannot use", &signInError{errors.New("the token has no refresh token — connect again")}, 412, true, "cannot be used (the token has no refresh token"},
		{"an unreadable token file", &signInError{fmt.Errorf("unreadable token file: %w", errors.New("bad JSON"))}, 412, true, "unreadable token file"},
		{"revoked", &oauthError{status: 400, message: "invalid_grant"}, 412, true, "revoked or has expired"},
		{"Google's token endpoint is down", &oauthError{status: 503, message: "backend_error"}, 503, false, "usually temporary"},
		{"Google refuses the call", &googleAPIError{status: 401, message: "Invalid Credentials"}, 502, true, "Google Calendar 401"},
		{"no such event", &googleAPIError{status: 404, message: "Not Found"}, 404, false, "Check the event and calendar IDs"},
		{"a calendar without busy times", &busyError{calendar: "team@example.com", reason: "notFound"}, 502, false, calendarCheckHint},
		{"a calendar missing from the answer", &busyError{calendar: "team@example.com"}, 502, false, "does not say when team@example.com is busy"},
		{"an answer that cannot be read", fmt.Errorf("reading Google Calendar's answer: %w", errors.New("unexpected EOF")), 502, false, "reading Google Calendar's answer"},
		{"Google unreachable", fmt.Errorf("could not reach Google Calendar (%w)", &net.DNSError{Err: "no such host", IsTemporary: true}), 503, false, "could not reach"},
		{"another calendar", fmt.Errorf("%w: %q", errCalendarNotAllowed, "x@example.com"), 403, false, "not enabled for this brain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, msg := CallProblem(tc.err)
			if status != tc.status || !strings.Contains(msg, tc.want) || strings.Contains(msg, calendarReconnectHint) != tc.reconnect {
				t.Errorf("%d %q; want %d with %q, reconnect hint %v", status, msg, tc.status, tc.want, tc.reconnect)
			}
		})
	}
}

// TestCalendarHintsNameTheDashboardsButtons: what a brain tells its owner
// to click is a button the brain's Calendar card shows: "Connect Google
// Calendar" until it is connected, "Reconnect" once Google refuses it.
func TestCalendarHintsNameTheDashboardsButtons(t *testing.T) {
	t.Parallel()
	js, err := os.ReadFile(filepath.Join(repoDir, "web", "assets", "components", "calendar.js"))
	if err != nil {
		t.Fatal(err)
	}
	_, notConnected := CallProblem(fs.ErrNotExist)
	for _, label := range []string{"Reconnect", "Connect Google Calendar"} {
		if !strings.Contains(calendarReconnectHint+notConnected, label) {
			t.Errorf("no hint names %q", label)
		}
		if !strings.Contains(string(js), "'"+label+"'") {
			t.Errorf("the dashboard has no %q button", label)
		}
	}
}

func TestTokenLifetime(t *testing.T) {
	for _, c := range []struct {
		in   int
		want time.Duration
	}{{3599, 3599 * time.Second}, {0, time.Hour}, {-5, time.Hour}} {
		if got := tokenLifetime(c.in); got != c.want {
			t.Errorf("tokenLifetime(%d) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestGoogleDecoderDiagnosticContracts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "false") }))
	defer server.Close()
	g := New(googleTestOptions(Options{CredsDir: func() string { return dir }}, server))
	if err := os.WriteFile(g.TokenPath("a"), []byte("false"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := g.AccessToken(t.Context(), "a")
	want := "unreadable token file: json: cannot unmarshal bool into Go value of type main.googleToken"
	if err == nil || err.Error() != want {
		t.Fatalf("token diagnostic=%v; want %q", err, want)
	}
	var typed *json.UnmarshalTypeError
	if !errors.As(err, &typed) {
		t.Fatal("token decoder metadata lost")
	}
	_, err = g.EventsOf(t.Context(), &Token{Token: "ACCESS-1"}, []string{"primary"}, time.Now(), time.Now().Add(time.Hour), "", 100)
	want = "reading Google Calendar's answer: json: cannot unmarshal bool into Go value of type struct { Items []main.googleEvent \"json:\\\"items\\\"\"; NextPageToken string \"json:\\\"nextPageToken\\\"\" }"
	if err == nil || err.Error() != want {
		t.Fatalf("event diagnostic=%v; want %q", err, want)
	}
	if !errors.As(err, &typed) {
		t.Fatal("event decoder metadata lost")
	}
}
