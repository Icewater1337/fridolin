package app

import (
	"encoding/json"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// The route classes, by what guards a route.
const (
	// classPublic answers anyone on this machine: liveness and readiness.
	classPublic = "public"
	// classScript is for local scripts and brains: the scripts token or a
	// run's, not the dashboard's, and the Host and Origin checks.
	classScript = "script"
	// classStatic is the dashboard page and its assets.
	classStatic = "static"
	// classAPI needs the dashboard token, and on writes the
	// X-App-Dashboard header and a JSON body.
	classAPI = "api"
	// classAPIMount is an unknown path under /api/: the token first, then
	// the API mux's 404.
	classAPIMount = "api-mount"
	// classLogin signs the dashboard in: no token, since it is how the
	// browser gets a session, but the X-App-Dashboard header, a JSON
	// body and the token or a one-time code in it.
	classLogin = "login"
	// classBuilder needs a builder brain's own token.
	classBuilder = "builder"
	// classBrainRun needs the token of a brain's run in progress.
	classBrainRun = "brain-run"
	// classGitHubFlow is a page of the GitHub App's manifest flow that the
	// browser is sent to: GET only, no token, and what it does is tied to
	// a one-time state an API call made (the install's return only sends
	// the browser on).
	classGitHubFlow = "github-flow"
	// classNotFound is a catch-all for unknown paths under a prefix: a
	// 404 for anyone, token or not.
	classNotFound = "not-found"
	// classTicket is a file by the ticket in its address (files_ticket.go):
	// no token, but a ticket the dashboard got through the API.
	classTicket = "ticket"
	// classSiteBridge is on the site bridge's own listener
	// (site_bridge.go): its token, and a site account.
	classSiteBridge = "site-bridge"
)

// httpRoute is one registered pattern. probe is a concrete request path for it,
// when the pattern has wildcards or a GET needs a query to stay off the
// network.
type httpRoute struct {
	method, pattern, class, probe string
}

// routeInventory is every route the bridge serves, written out by hand so
// that a route that appears, disappears or changes its guard is a decision.
var routeInventory = []httpRoute{
	{"GET", "/health", classPublic, ""},
	{"GET", "/ready", classPublic, ""},
	{"POST", "/send", classScript, ""},
	{"POST", "/ingest", classScript, ""},
	{"POST", "/otp/take", classScript, ""},
	{"", "/", classStatic, ""},
	{"GET", "/github/connect", classGitHubFlow, ""},
	{"GET", "/github/created", classGitHubFlow, "/github/created?code=x&state=nope"},
	{"GET", "/github/installed", classGitHubFlow, ""},

	// The builder's own routes, on a mux of their own mounted at
	// /builder/. That mux has no catch-all, so this entry is the mount: an
	// unknown call there gets the mux's plain 404.
	{"", "/builder/", classNotFound, "/builder/nope"},
	{"GET", "/builder/context", classBuilder, ""},
	{"POST", "/builder/check", classBuilder, ""},
	{"POST", "/builder/preview", classBuilder, ""},
	{"POST", "/builder/sync", classBuilder, ""},
	{"POST", "/builder/propose", classBuilder, ""},

	// What a brain's tools ask the bridge for, on a mux of their own
	// mounted at /brain/. Its catch-all makes an unknown call there a JSON
	// 404.
	{"", "/brain/", classNotFound, "/brain/nope"},
	{"GET", "/brain/polls", classBrainRun, ""},
	{"GET", "/brain/calendar/calendars", classBrainRun, ""},
	{"GET", "/brain/calendar/events", classBrainRun, ""},
	{"POST", "/brain/calendar/events", classBrainRun, ""},
	{"DELETE", "/brain/calendar/events/{id}", classBrainRun, "/brain/calendar/events/e1"},
	{"POST", "/brain/calendar/freebusy", classBrainRun, ""},
	{"GET", "/brain/mcp/tools", classBrainRun, ""},
	{"POST", "/brain/mcp/call", classBrainRun, ""},
	{"GET", "/brain/memory/search", classBrainRun, ""},
	{"GET", "/brain/intents", classBrainRun, ""},
	{"POST", "/brain/intents", classBrainRun, ""},
	{"DELETE", "/brain/intents/{id}", classBrainRun, "/brain/intents/i1"},

	// Outside the API's guard, on the outer mux.
	{"POST", "/api/login", classLogin, ""},
	{"GET", "/files/raw", classTicket, "/files/raw?root=brain:main&path=x"},

	// The site's /files and /wiki, on the site bridge's own listener.
	{"", "/site-api/", classSiteBridge, "/site-api/nope"},
	{"GET", "/site-api/files/roots", classSiteBridge, ""},
	{"GET", "/site-api/files/list", classSiteBridge, ""},
	{"GET", "/site-api/files/search", classSiteBridge, ""},
	{"GET", "/site-api/files/view", classSiteBridge, ""},
	{"GET", "/site-api/files/raw", classSiteBridge, ""},
	{"GET", "/site-api/wiki", classSiteBridge, ""},
	{"GET", "/site-api/wiki/search", classSiteBridge, ""},
	{"GET", "/site-api/wiki/{brain}/pages", classSiteBridge, "/site-api/wiki/main/pages"},
	{"GET", "/site-api/wiki/{brain}/page", classSiteBridge, "/site-api/wiki/main/page"},

	{"GET", "/api/status", classAPI, ""},
	{"POST", "/api/logout", classAPI, ""},
	{"POST", "/api/login/code", classAPI, ""},
	{"POST", "/api/token/rotate", classAPI, ""},
	{"GET", "/api/whatsapp/qr", classAPI, ""},
	{"POST", "/api/whatsapp/pair", classAPI, ""},
	{"GET", "/api/whatsapp/groups", classAPI, ""},
	{"GET", "/api/activity", classAPI, ""},
	{"GET", "/api/logs", classAPI, ""},
	{"GET", "/api/chats/ignored", classAPI, ""},
	{"POST", "/api/chats/allow", classAPI, ""},
	{"POST", "/api/chats/revoke", classAPI, ""},
	{"POST", "/api/chats/members", classAPI, ""},
	{"POST", "/api/chats/dismiss", classAPI, ""},
	{"GET", "/api/config", classAPI, ""},
	{"PUT", "/api/config", classAPI, ""},
	{"POST", "/api/restart", classAPI, ""},
	{"GET", "/api/brains", classAPI, ""},
	{"GET", "/api/brains/{id}", classAPI, "/api/brains/main"},
	{"POST", "/api/brains/inspect", classAPI, ""},
	{"POST", "/api/brains", classAPI, ""},
	{"PUT", "/api/brains/{id}", classAPI, "/api/brains/main"},
	{"POST", "/api/brains/{id}/approve", classAPI, "/api/brains/main/approve"},
	{"DELETE", "/api/brains/{id}", classAPI, "/api/brains/main"},
	{"DELETE", "/api/brains/{id}/intents/{intent}", classAPI, "/api/brains/main/intents/i1"},
	{"POST", "/api/brains/{id}/stop", classAPI, "/api/brains/main/stop"},
	{"DELETE", "/api/brain-dirs", classAPI, ""},
	{"GET", "/api/fs/capabilities", classAPI, ""},
	{"POST", "/api/fs/pick", classAPI, ""},
	{"GET", "/api/fs/list", classAPI, ""},
	{"GET", "/api/backup/status", classAPI, ""},
	{"POST", "/api/backup/connect", classAPI, ""},
	{"POST", "/api/backup/disconnect", classAPI, ""},
	{"PUT", "/api/backup/drive-client", classAPI, ""},
	{"DELETE", "/api/backup/drive-client", classAPI, ""},
	{"PUT", "/api/backup/settings", classAPI, ""},
	{"POST", "/api/backup/run", classAPI, ""},
	{"POST", "/api/backup/cancel", classAPI, ""},
	{"PUT", "/api/brains/{id}/backup", classAPI, "/api/brains/main/backup"},
	{"GET", "/api/brains/{id}/backup/versions", classAPI, "/api/brains/main/backup/versions"},
	{"POST", "/api/backup/restore", classAPI, ""},
	{"GET", "/api/vault/status", classAPI, ""},
	{"POST", "/api/vault/sync", classAPI, ""},
	{"POST", "/api/vault/evict", classAPI, ""},
	{"POST", "/api/vault/rebuild", classAPI, ""},
	{"POST", "/api/vault/get", classAPI, ""},
	{"POST", "/api/vault/forget", classAPI, ""},
	{"POST", "/api/vault/snapshot", classAPI, ""},
	{"POST", "/api/vault/check", classAPI, ""},
	{"POST", "/api/vault/restore-drill", classAPI, ""},
	{"POST", "/api/vault/prune/allow", classAPI, ""},
	{"POST", "/api/vault/migrate-encryption", classAPI, ""},
	{"POST", "/api/vault/targets", classAPI, ""},
	{"PUT", "/api/vault/targets/{name}", classAPI, ""},
	{"DELETE", "/api/vault/targets/{name}", classAPI, ""},
	{"POST", "/api/vault/targets/{name}/snapshot", classAPI, ""},
	{"POST", "/api/vault/targets/{name}/check", classAPI, ""},
	{"POST", "/api/vault/targets/{name}/restore-drill", classAPI, ""},
	{"POST", "/api/vault/targets/{name}/prune/allow", classAPI, ""},
	{"GET", "/api/google/client", classAPI, ""},
	{"PUT", "/api/google/client", classAPI, ""},
	{"DELETE", "/api/google/client", classAPI, ""},
	{"GET", "/api/brains/{id}/calendar", classAPI, "/api/brains/main/calendar?list=0"},
	{"POST", "/api/brains/{id}/calendar/connect", classAPI, "/api/brains/main/calendar/connect"},
	{"PUT", "/api/brains/{id}/calendar", classAPI, "/api/brains/main/calendar"},
	{"DELETE", "/api/brains/{id}/calendar", classAPI, "/api/brains/main/calendar"},
	{"GET", "/api/plaud/status", classAPI, ""},
	{"POST", "/api/plaud/connect", classAPI, ""},
	{"POST", "/api/plaud/disconnect", classAPI, ""},
	{"PUT", "/api/plaud/settings", classAPI, ""},
	{"POST", "/api/plaud/sync", classAPI, ""},
	{"POST", "/api/plaud/resend", classAPI, ""},
	{"PUT", "/api/plaud/app/credentials", classAPI, ""},
	{"POST", "/api/plaud/app/build", classAPI, ""},
	{"GET", "/api/plaud/app.apk", classAPI, ""},
	{"DELETE", "/api/plaud/devices/{id}", classAPI, ""},
	{"GET", "/api/site", classAPI, ""},
	{"PUT", "/api/site/settings", classAPI, ""},
	{"POST", "/api/site/restart", classAPI, ""},
	{"POST", "/api/site/deploy-key", classAPI, ""},
	{"POST", "/api/site/deploy-key/github", classAPI, ""},
	{"POST", "/api/site/github-repo", classAPI, ""},
	{"PUT", "/api/site/repo", classAPI, ""},
	{"DELETE", "/api/site/repo", classAPI, ""},
	{"POST", "/api/site/repo/starter", classAPI, ""},
	{"POST", "/api/site/fetch", classAPI, ""},
	{"POST", "/api/site/deploy", classAPI, ""},
	{"GET", "/api/site/users", classAPI, ""},
	{"POST", "/api/site/users", classAPI, ""},
	{"PUT", "/api/site/users/{id}", classAPI, "/api/site/users/u1"},
	{"DELETE", "/api/site/users/{id}", classAPI, "/api/site/users/u1"},
	{"PUT", "/api/site/roles", classAPI, ""},
	{"PUT", "/api/site/tunnel", classAPI, ""},
	{"POST", "/api/site/check-domain", classAPI, ""},
	{"PUT", "/api/site/google-client", classAPI, ""},
	{"DELETE", "/api/site/google-client", classAPI, ""},
	{"GET", "/api/site/secrets", classAPI, ""},
	{"PUT", "/api/site/secrets/{module}/{name}", classAPI, "/api/site/secrets/m1/TOKEN"},
	{"DELETE", "/api/site/secrets/{module}/{name}", classAPI, "/api/site/secrets/m1/TOKEN"},
	{"GET", "/api/site/builders", classAPI, ""},
	{"PUT", "/api/site/builders/{id}", classAPI, "/api/site/builders/main"},
	{"GET", "/api/site/proposals", classAPI, ""},
	{"POST", "/api/site/proposals/{id}/merge", classAPI, "/api/site/proposals/p1/merge"},
	{"DELETE", "/api/site/proposals/{id}", classAPI, "/api/site/proposals/p1"},
	{"POST", "/api/site/previews/{id}", classAPI, "/api/site/previews/main"},
	{"GET", "/api/site/build/{id}", classAPI, "/api/site/build/main"},
	{"POST", "/api/site/build/{id}", classAPI, "/api/site/build/main"},
	{"POST", "/api/site/build/{id}/new", classAPI, "/api/site/build/main/new"},
	{"POST", "/api/site/build/{id}/stop", classAPI, "/api/site/build/main/stop"},
	{"GET", "/api/github", classAPI, ""},
	{"POST", "/api/github/connect", classAPI, ""},
	{"POST", "/api/github/test", classAPI, ""},
	{"POST", "/api/github/disconnect", classAPI, ""},
	{"GET", "/api/nodes", classAPI, ""},
	{"POST", "/api/nodes/join-code", classAPI, ""},
	{"POST", "/api/nodes/{id}/approve", classAPI, "/api/nodes/n1/approve"},
	{"POST", "/api/nodes/{id}/approve-commands", classAPI, "/api/nodes/n1/approve-commands"},
	{"POST", "/api/nodes/{id}/revoke", classAPI, "/api/nodes/n1/revoke"},
	{"PUT", "/api/nodes/{id}/tags", classAPI, "/api/nodes/n1/tags"},
	{"POST", "/api/nodes/{id}/call", classAPI, "/api/nodes/n1/call"},
	{"POST", "/api/nodes/{id}/update", classAPI, "/api/nodes/n1/update"},
	{"PUT", "/api/nodes/{id}/update-policy", classAPI, "/api/nodes/n1/update-policy"},
	{"GET", "/api/policy", classAPI, ""},
	{"POST", "/api/policy/lockdown", classAPI, ""},
	{"GET", "/api/threads", classAPI, ""},
	{"GET", "/api/audit", classAPI, ""},
	{"GET", "/api/approvals", classAPI, ""},
	{"POST", "/api/approvals/{id}", classAPI, "/api/approvals/a1"},
	{"GET", "/api/brains/{id}/skills", classAPI, "/api/brains/main/skills"},
	{"POST", "/api/brains/{id}/skills/approve", classAPI, "/api/brains/main/skills/approve"},
	{"PUT", "/api/brains/{id}/skills/policy", classAPI, "/api/brains/main/skills/policy"},
	{"GET", "/api/files/roots", classAPI, ""},
	{"GET", "/api/files/list", classAPI, ""},
	{"GET", "/api/files/search", classAPI, ""},
	{"GET", "/api/files/view", classAPI, ""},
	{"GET", "/api/files/raw", classAPI, ""},
	{"GET", "/api/files/ticket", classAPI, ""},
	{"GET", "/api/wiki", classAPI, ""},
	{"GET", "/api/wiki/search", classAPI, ""},
	{"GET", "/api/wiki/{brain}/pages", classAPI, "/api/wiki/main/pages"},
	{"GET", "/api/wiki/{brain}/page", classAPI, "/api/wiki/main/page"},
	{"GET", "/api/housekeeping", classAPI, ""},
	{"POST", "/api/housekeeping/run", classAPI, ""},
	{"POST", "/api/dev/inbound", classAPI, ""},
	{"GET", "/api/dev/outbox", classAPI, ""},
	{"POST", "/api/dev/vote", classAPI, ""},
	// The mount: an unknown /api/ path is the API mux's 404, not the page.
	{"", "/api/", classAPIMount, "/api/nope"},
}

// mountsWithCatchAll are the patterns registered twice: newHTTPHandler
// hands the prefix to a mux of its own, which has a catch-all at the same
// pattern. routeInventory lists the catch-all, and this the mount. Any
// other pattern registered twice is a route on a second mux, outside the
// guards of the first.
var mountsWithCatchAll = []string{"/brain/"}

// path is the request path to probe r with.
func (r httpRoute) path() string {
	if r.probe != "" {
		return r.probe
	}
	return r.pattern
}

// handlerPatternRE finds a route registration in the source: the pattern
// string given to HandleFunc or Handle.
var handlerPatternRE = regexp.MustCompile(`\.Handle(?:Func)?\("((?:[A-Z]+ )?/[^"]*)"`)

// TestRouteInventoryIsComplete compares the inventory with every route the
// module's code registers, wherever its files are, so that a new route
// has to be added there with its class. It counts each registration: the
// same pattern registered again on another mux would answer there without
// that mux's guards.
func TestRouteInventoryIsComplete(t *testing.T) {
	root := repoDir
	for !fsx.Exists(filepath.Join(root, "go.mod")) {
		if filepath.Dir(root) == root {
			t.Fatal("no go.mod above the package")
		}
		root = filepath.Dir(root)
	}
	var registered []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) && path != root {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range handlerPatternRE.FindAllStringSubmatch(string(data), -1) {
			registered = append(registered, m[1])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	listed := slices.Clone(mountsWithCatchAll)
	for _, r := range routeInventory {
		listed = append(listed, strings.TrimSpace(r.method+" "+r.pattern))
	}
	times := func(patterns []string) map[string]int {
		n := map[string]int{}
		for _, p := range patterns {
			n[p]++
		}
		return n
	}
	reg, lst := times(registered), times(listed)
	for _, p := range slices.Sorted(maps.Keys(reg)) {
		switch {
		case lst[p] == 0:
			t.Errorf("route %q is registered but not in routeInventory", p)
		case lst[p] != reg[p]:
			t.Errorf("route %q: %d registrations, %d in routeInventory and mountsWithCatchAll", p, reg[p], lst[p])
		}
	}
	for _, p := range slices.Sorted(maps.Keys(lst)) {
		if reg[p] == 0 {
			t.Errorf("route %q is in routeInventory but nothing registers it", p)
		}
	}
}

// reachesOutside are the routes whose handlers act outside the test: they
// re-execute the process, open a folder picker on the desktop, pair a
// phone with WhatsApp, reach GitHub or a git remote, or restore a backup
// over a brain's folder. TestRouteInventoryGuards only looks them up in
// the API mux and never sends them a request, which a guard that stopped
// working would let through. TestRestartInATestFailsTheRun probes the
// restart for real, in a test binary of its own.
var reachesOutside = map[string]bool{
	"POST /api/restart":                true,
	"POST /api/fs/pick":                true,
	"POST /api/whatsapp/pair":          true,
	"PUT /api/site/repo":               true,
	"POST /api/site/repo/starter":      true,
	"POST /api/site/fetch":             true,
	"POST /api/site/deploy":            true,
	"POST /api/site/deploy-key/github": true,
	"POST /api/site/github-repo":       true,
	"POST /api/backup/restore":         true,
	"POST /api/github/test":            true,
	"POST /api/site/check-domain":      true,
}

// TestRouteInventoryGuards probes every route of the inventory for the
// guard its class promises. Each probe breaks one guard only, so a guard
// that stopped working lets the probe through to the route's handler. The
// guards are therefore checked first on routes whose handlers change
// nothing, and the test stops there if one fails. The routes that reach
// outside the test are only looked up in the API mux: the guards wrap that
// mux as a whole (newHTTPHandler), the first check shows they hold, and
// TestRouteInventoryIsComplete fails when a pattern is registered on a
// second mux as well, outside them.
func TestRouteInventoryGuards(t *testing.T) {
	h, _, _ := goldenBridge(t)
	api, ok := h.b.apiMux().(*http.ServeMux)
	if !ok {
		t.Fatal("the API handler is no ServeMux")
	}
	token := h.token()
	// What a GET must never show: the dashboard token, and the secrets
	// the golden bridge holds (the Google client's, the calendar token's).
	secrets := []string{token, "TEST-CLIENT-SECRET", "golden-refresh-token", "golden-access-token"}

	request := func(method, path, body string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+config.DefaultHTTPListen+path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		for _, m := range mutate {
			m(r)
		}
		w := httptest.NewRecorder()
		h.h.ServeHTTP(w, r)
		return w
	}
	withToken := func(r *http.Request) { r.Header.Set("X-Bridge-Token", token) }
	asBearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
	asScript := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+h.readToken("scripts.token")) }
	asDashboard := func(r *http.Request) { r.Header.Set("X-Bridge-Dashboard", "1") }
	asForm := func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }
	fromElsewhere := func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.com") }
	underOtherHost := func(r *http.Request) { r.Host = "evil.example.com:8765" }

	// Every guard on a route whose handler only reads or refuses: inspect
	// looks at a folder, an empty /send is refused, the builder's context
	// is read.
	for _, c := range []struct {
		guard, method, path, body string
		mutate                    []func(*http.Request)
		want                      int
	}{
		{"the token", "GET", "/api/status", "", nil, http.StatusUnauthorized},
		{"X-Bridge-Dashboard", "POST", "/api/brains/inspect", "{}", []func(*http.Request){withToken}, http.StatusForbidden},
		{"a JSON body", "POST", "/api/brains/inspect", "a=b", []func(*http.Request){withToken, asDashboard, asForm}, http.StatusUnsupportedMediaType},
		{"the Origin, on the API", "POST", "/api/brains/inspect", "{}", []func(*http.Request){withToken, asDashboard, fromElsewhere}, http.StatusForbidden},
		{"the Origin, for scripts", "POST", "/send", "{}", []func(*http.Request){fromElsewhere}, http.StatusForbidden},
		{"the Host", "POST", "/send", "{}", []func(*http.Request){underOtherHost}, http.StatusMisdirectedRequest},
		{"the builder's token", "GET", "/builder/context", "", nil, http.StatusUnauthorized},
		{"the builder's token, not the dashboard's", "GET", "/builder/context", "", []func(*http.Request){withToken}, http.StatusUnauthorized},
		{"a run's token", "GET", "/brain/polls", "", nil, http.StatusUnauthorized},
		{"a run's token, not the dashboard's", "GET", "/brain/polls", "", []func(*http.Request){withToken, asBearer}, http.StatusUnauthorized},
	} {
		if w := request(c.method, c.path, c.body, c.mutate...); w.Code != c.want {
			t.Errorf("guard %s: %s %s answered %d, want %d", c.guard, c.method, c.path, w.Code, c.want)
		}
	}
	if t.Failed() {
		t.Fatal("a guard does not hold on a route that changes nothing; the other routes are not probed, since their handlers would run")
	}

	for _, rt := range routeInventory {
		method := rt.method
		if method == "" {
			method = "GET"
		}
		t.Run(strings.TrimSpace(rt.method+" "+rt.pattern), func(t *testing.T) {
			switch rt.class {
			case classAPI:
				// The API mux says which pattern takes the request, without
				// running it: a route missing from the mux would fall to
				// the catch-all.
				r := httptest.NewRequest(method, "http://"+config.DefaultHTTPListen+rt.path(), nil)
				if _, pattern := api.Handler(r); pattern != strings.TrimSpace(rt.method+" "+rt.pattern) {
					t.Errorf("%s %s is served by %q", method, rt.path(), pattern)
				}
				if reachesOutside[rt.method+" "+rt.pattern] {
					break
				}
				if w := request(method, rt.path(), ""); w.Code != http.StatusUnauthorized {
					t.Errorf("without the token: %d, want 401", w.Code)
				}
				if method == "GET" {
					break
				}
				if w := request(method, rt.path(), "{}", withToken); w.Code != http.StatusForbidden {
					t.Errorf("without X-Bridge-Dashboard: %d, want 403", w.Code)
				}
				if w := request(method, rt.path(), "a=b", withToken, asDashboard, asForm); w.Code != http.StatusUnsupportedMediaType {
					t.Errorf("a form body: %d, want 415", w.Code)
				}
				if w := request(method, rt.path(), "{}", withToken, asDashboard, fromElsewhere); w.Code != http.StatusForbidden {
					t.Errorf("from another site: %d, want 403", w.Code)
				}
			case classScript:
				if w := request(method, rt.path(), "{}", fromElsewhere); w.Code != http.StatusForbidden {
					t.Errorf("from another site: %d, want 403", w.Code)
				}
				if w := request(method, rt.path(), "{}", underOtherHost); w.Code != http.StatusMisdirectedRequest {
					t.Errorf("under another host name: %d, want 421", w.Code)
				}
				if w := request(method, rt.path(), "{}", asBearer); w.Code != http.StatusUnauthorized {
					t.Errorf("with the dashboard token: %d, want 401", w.Code)
				}
				if w := request(method, rt.path(), "{}", asScript); w.Code != http.StatusBadRequest {
					t.Errorf("an empty request with the scripts token: %d, want the handler's 400", w.Code)
				}
			case classBuilder:
				if w := request(method, rt.path(), "{}"); w.Code != http.StatusUnauthorized {
					t.Errorf("without a builder token: %d, want 401", w.Code)
				}
				if w := request(method, rt.path(), "{}", withToken); w.Code != http.StatusUnauthorized {
					t.Errorf("with the dashboard token: %d, want 401", w.Code)
				}
			case classBrainRun:
				if w := request(method, rt.path(), "{}"); w.Code != http.StatusUnauthorized {
					t.Errorf("without a run's token: %d, want 401", w.Code)
				}
				if w := request(method, rt.path(), "{}", withToken, asBearer); w.Code != http.StatusUnauthorized {
					t.Errorf("with the dashboard token: %d, want 401", w.Code)
				}
				if w := request(method, rt.path(), "{}", fromElsewhere); method != "GET" && w.Code != http.StatusForbidden {
					t.Errorf("from another site: %d, want 403", w.Code)
				}
			case classAPIMount:
				r := httptest.NewRequest(method, "http://"+config.DefaultHTTPListen+rt.path(), nil)
				if _, pattern := api.Handler(r); pattern != "" {
					t.Errorf("%s %s is served by %q", method, rt.path(), pattern)
				}
				if w := request(method, rt.path(), ""); w.Code != http.StatusUnauthorized {
					t.Errorf("without the token: %d, want 401", w.Code)
				}
				if w := request(method, rt.path(), "", withToken); w.Code != http.StatusNotFound {
					t.Errorf("with the token: %d, want 404", w.Code)
				}
			case classLogin:
				if w := request(method, rt.path(), "{}"); w.Code != http.StatusForbidden {
					t.Errorf("without X-Bridge-Dashboard: %d, want 403", w.Code)
				}
				if w := request(method, rt.path(), "a=b", asDashboard, asForm); w.Code != http.StatusUnsupportedMediaType {
					t.Errorf("a form body: %d, want 415", w.Code)
				}
				if w := request(method, rt.path(), "{}", asDashboard, fromElsewhere); w.Code != http.StatusForbidden {
					t.Errorf("from another site: %d, want 403", w.Code)
				}
				if w := request(method, rt.path(), `{"code":"nope"}`, asDashboard); w.Code != http.StatusUnauthorized {
					t.Errorf("a wrong code: %d, want 401", w.Code)
				}
			case classNotFound:
				if w := request(method, rt.path(), ""); w.Code != http.StatusNotFound {
					t.Errorf("without a token: %d, want 404", w.Code)
				}
				if w := request(method, rt.path(), "", withToken, asBearer); w.Code != http.StatusNotFound {
					t.Errorf("with the dashboard token: %d, want 404", w.Code)
				}
			case classGitHubFlow:
				// Without a state it made, a page refuses (or only sends the
				// browser on); with the dashboard's token it does no more.
				for _, mutate := range [][]func(*http.Request){nil, {withToken}} {
					w := request(method, rt.path(), "", mutate...)
					if w.Code != http.StatusBadRequest && w.Code != http.StatusSeeOther {
						t.Errorf("an unknown state: %d, want 400 or 303", w.Code)
					}
					if loc := w.Header().Get("Location"); w.Code == http.StatusSeeOther && loc != "/#/integrations" {
						t.Errorf("sent the browser to %q", loc)
					}
				}
				if w := request("POST", rt.path(), "{}"); w.Code != http.StatusMethodNotAllowed {
					t.Errorf("POST: %d, want 405", w.Code)
				}
			case classTicket:
				if w := request(method, rt.path(), ""); w.Code != http.StatusUnauthorized {
					t.Errorf("without a ticket: %d, want 401", w.Code)
				}
				if w := request(method, rt.path()+"&ticket="+token, "", withToken); w.Code != http.StatusUnauthorized {
					t.Errorf("with the dashboard token for a ticket: %d, want 401", w.Code)
				}
			case classSiteBridge:
				url, siteToken, err := h.b.filesBridge()
				if err != nil {
					t.Fatal(err)
				}
				if code, _ := siteBridgeRequest(t, url, "", "u_x", rt.path()); code != http.StatusUnauthorized {
					t.Errorf("without its token: %d, want 401", code)
				}
				if code, _ := siteBridgeRequest(t, url, token, "u_x", rt.path()); code != http.StatusUnauthorized {
					t.Errorf("with the dashboard token: %d, want 401", code)
				}
				want := http.StatusUnauthorized
				if rt.method == "" {
					want = http.StatusNotFound
				}
				if code, _ := siteBridgeRequest(t, url, siteToken, "u_nobody", rt.path()); code != want {
					t.Errorf("for no site account: %d, want %d", code, want)
				}
				if w := request(method, rt.path(), "", withToken); w.Code != http.StatusNotFound {
					t.Errorf("on the dashboard's listener: %d, want 404", w.Code)
				}
			case classPublic, classStatic:
				if w := request(method, rt.path(), ""); w.Code != http.StatusOK {
					t.Errorf("without a token: %d, want 200", w.Code)
				}
				if w := request("POST", rt.path(), "{}"); rt.class == classStatic && w.Code != http.StatusMethodNotAllowed {
					t.Errorf("POST: %d, want 405", w.Code)
				}
			default:
				t.Fatalf("unknown class %q", rt.class)
			}
			if method == "GET" {
				w := request("GET", rt.path(), "", withToken)
				for _, secret := range secrets {
					if strings.Contains(w.Body.String(), secret) {
						t.Errorf("GET %s shows a secret: %s", rt.path(), w.Body.String())
					}
				}
				if rt.class == classAPI && strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") && !json.Valid(w.Body.Bytes()) {
					t.Errorf("GET %s: not JSON: %s", rt.path(), w.Body.String())
				}
			}
		})
	}
	for p := range reachesOutside {
		if !slices.ContainsFunc(routeInventory, func(r httpRoute) bool { return r.method+" "+r.pattern == p }) {
			t.Errorf("reachesOutside lists %q, which is not in routeInventory", p)
		}
	}
}
