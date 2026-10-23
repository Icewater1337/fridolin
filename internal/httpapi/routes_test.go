package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Keep the public method/path inventory reviewable independently of handlers.
// Command integration tests additionally verify each route's authentication class.
func TestRouteTableMatchesGolden(t *testing.T) {
	source, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`\.Handle(?:Func)?\("((?:[A-Z]+ )?/[^\"]*)"`)
	var routes []string
	for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
		routes = append(routes, match[1])
	}
	slices.Sort(routes)
	want, err := os.ReadFile("testdata/routes.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(routes, "\n") + "\n"; got != string(want) {
		t.Errorf("HTTP route inventory changed:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// /otp/take is for scripts on this machine: the tailnet surface does not
// serve it, so no token reaches a code from another device.
func TestTailnetDoesNotServeOTPTake(t *testing.T) {
	const tailnet = "fridolin-core.example.ts.net"
	for _, remote := range []bool{false, true} {
		took := false
		noop := func(http.ResponseWriter, *http.Request) {}
		d := Deps{
			Listen: "127.0.0.1:8765", Remote: remote, Hosts: map[string]bool{tailnet: true},
			Ready: noop, Send: noop, Ingest: noop,
			TakeOTP:           func(w http.ResponseWriter, _ *http.Request) { took = true; w.WriteHeader(http.StatusNotFound) },
			Builder:           func() http.Handler { return http.NotFoundHandler() },
			Brain:             func() http.Handler { return http.NotFoundHandler() },
			DashboardDisabled: func() bool { return true },
		}
		r := httptest.NewRequest("POST", "/otp/take", strings.NewReader(`{"request":"0123456789abcdef"}`))
		r.Host = "127.0.0.1:8765"
		if remote {
			r.Host = tailnet
		}
		r.Header.Set("Authorization", "Bearer x")
		w := httptest.NewRecorder()
		New(d).ServeHTTP(w, r)
		if took == remote {
			t.Errorf("remote %v: the handler ran %v (status %d)", remote, took, w.Code)
		}
	}
}
