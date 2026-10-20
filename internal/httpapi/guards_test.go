package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPHostGuard(t *testing.T) {
	hosts := AllowedHosts("127.0.0.1:8765")
	for _, tc := range []struct {
		host string
		want int
	}{
		{"127.0.0.1:8765", 204}, {"LOCALHOST:8765", 204}, {"[::1]:8765", 204},
		{"evil.example.com:8765", 421}, {"localhost:9999", 421}, {"localhost", 421},
	} {
		t.Run(tc.host, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.Host = tc.host
			w := httptest.NewRecorder()
			HostGuard(hosts, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
			}
		})
	}
}

// On the tailnet the dashboard may be served with TLS: an https origin of
// an allowed host passes there, and only there; a request without a token
// to /send is refused before the handler.
func TestRemoteGuards(t *testing.T) {
	hosts := map[string]bool{"fridolin-core.example.ts.net": true}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	for _, tc := range []struct {
		origin string
		want   int
	}{{"https://fridolin-core.example.ts.net", 204}, {"http://fridolin-core.example.ts.net", 204}, {"https://evil.example", 403}} {
		r := httptest.NewRequest("POST", "/", nil)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		originGuard(hosts, []string{"http://", "https://"}, ok).ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("origin %s: %d, want %d", tc.origin, w.Code, tc.want)
		}
	}
	w := httptest.NewRecorder()
	RequireAuthorization(ok)(w, httptest.NewRequest("POST", "/send", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("/send without a token: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "/send", nil)
	r.Header.Set("Authorization", "Bearer x")
	w = httptest.NewRecorder()
	RequireAuthorization(ok)(w, r)
	if w.Code != 204 {
		t.Errorf("/send with a token: %d", w.Code)
	}
}

func TestHTTPOriginGuard(t *testing.T) {
	for _, tc := range []struct {
		name, method, origin, site string
		want                       int
	}{
		{"script", "POST", "", "", 204}, {"same origin", "POST", "HTTP://LOCALHOST:8765", "same-origin", 204},
		{"navigation", "POST", "http://[::1]:8765", "none", 204}, {"wrong origin", "POST", "https://localhost:8765", "", 403},
		{"wrong port", "POST", "http://localhost:9999", "", 403}, {"null origin", "POST", "null", "", 403},
		{"cross site", "POST", "", "cross-site", 403}, {"same site", "POST", "", "same-site", 403},
		{"read unchanged", "GET", "null", "cross-site", 204}, {"head unchanged", "HEAD", "null", "cross-site", 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/", nil)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			w := httptest.NewRecorder()
			OriginGuard(AllowedHosts("127.0.0.1:8765"), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
			}
		})
	}
}

func TestHTTPAPIGuard(t *testing.T) {
	token, err := LoadDashboardToken(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, token, header, contentType, body string
		required, session                              bool
		want                                           int
	}{
		{name: "token", method: "GET", token: token.Value(), required: true, want: 204},
		{name: "session", method: "GET", required: true, session: true, want: 204},
		{name: "missing", method: "GET", required: true, want: 401},
		{name: "wrong", method: "GET", token: "wrong", required: true, want: 401},
		{name: "auth disabled", method: "GET", want: 204},
		{name: "head", method: "HEAD", token: token.Value(), required: true, want: 204},
		{name: "write missing header", method: "POST", token: token.Value(), required: true, want: 403},
		{name: "write wrong type", method: "POST", token: token.Value(), header: "1", contentType: "text/plain", body: "{}", required: true, want: 415},
		{name: "json", method: "POST", header: "1", contentType: "application/json; charset=utf-8", body: "{}", required: true, session: true, want: 204},
		{name: "empty write", method: "POST", header: "1", required: true, session: true, want: 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/", strings.NewReader(tc.body))
			r.Header.Set("X-Bridge-Token", tc.token)
			r.Header.Set("X-Bridge-Dashboard", tc.header)
			r.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			APIGuard(token, func() bool { return tc.required }, func(*http.Request) bool { return tc.session }, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
			if w.Code != tc.want || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
			}
		})
	}
}

func TestHTTPAPIGuardReadsCurrentPolicy(t *testing.T) {
	token, err := LoadDashboardToken(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	required, session := false, false
	handler := APIGuard(token, func() bool { return required }, func(*http.Request) bool { return session }, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		required, session bool
		want              int
	}{{false, false, 204}, {true, false, 401}, {true, true, 204}, {true, false, 401}} {
		required, session = tc.required, tc.session
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code != tc.want {
			t.Fatalf("required=%v session=%v: %d", required, session, w.Code)
		}
	}
}

func TestHTTPWriteBodyLimit(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", (1<<20)+1)))
	r.Header.Set("X-Bridge-Dashboard", "1")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	if !CheckWrite(w, r) {
		t.Fatal("valid write headers refused")
	}
	data, err := io.ReadAll(r.Body)
	if err == nil || len(data) != 1<<20 {
		t.Fatalf("body length=%d error=%v", len(data), err)
	}
}

func TestHTTPSecurityHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	for k, v := range map[string]string{
		"Content-Security-Policy": dashboardCSP, "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer",
		"X-Frame-Options": "DENY", "Permissions-Policy": "camera=(), microphone=(), geolocation=()", "Cross-Origin-Opener-Policy": "same-origin", "Cross-Origin-Resource-Policy": "same-origin",
	} {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s=%q want %q", k, got, v)
		}
	}
	if w.Code != 204 {
		t.Fatalf("next handler did not run: %d", w.Code)
	}
}
