package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/doctor"
)

// The node checks apply with nodes.enabled: the port must answer (M2's
// gate), and approved nodes not heard from for 5 minutes are a warning.
func TestDoctorNodeChecks(t *testing.T) {
	t.Parallel()
	off := doctor.Run(context.Background(), healthyMac().doctorEnv(doctorConfig(nil), nil), DoctorRegistry.Checks())
	for _, c := range off.Checks {
		if strings.HasPrefix(c.ID, "nodes-") {
			t.Errorf("%s runs with nodes off", c.ID)
		}
	}
	on := func(listen string) *config.Settings {
		return doctorConfig(func(c *config.Settings) { c.Nodes = config.NodesConfig{Enabled: true, Listen: listen} })
	}
	const registry = "/home/you/.fridolin/nodes/nodes.json"
	tests := []struct {
		name      string
		listen    string
		listening string
		nodesJSON string
		id        string
		status    string
		detail    string
		ok        bool
	}{
		{"port up", ":8766", "127.0.0.1:8766", "", "nodes-port", doctor.OK, "answers on 127.0.0.1:8766", true},
		{"port up on an address", "100.64.0.1:9000", "100.64.0.1:9000", "", "nodes-port", doctor.OK, "100.64.0.1:9000", true},
		{"port down", ":8766", "", "", "nodes-port", doctor.Fail, "nothing answers on 127.0.0.1:8766", false},
		{"no registry", ":8766", "127.0.0.1:8766", "", "nodes-seen", doctor.OK, "no nodes yet", true},
		{"only pending", ":8766", "127.0.0.1:8766", `{"nodes":[{"id":"a","name":"A","device":"pending"}]}`, "nodes-seen", doctor.OK, "no approved nodes", true},
		{"fresh", ":8766", "127.0.0.1:8766", `{"nodes":[{"id":"a","name":"A","device":"approved","last_seen":"2026-10-20T11:58:00Z"},{"id":"r","name":"R","device":"revoked","last_seen":"2026-01-01T00:00:00Z"}]}`, "nodes-seen", doctor.OK, "1 approved nodes, each heard from", true},
		{"stale", ":8766", "127.0.0.1:8766", `{"nodes":[{"id":"a","name":"A","device":"approved","last_seen":"2026-10-20T11:00:00Z"},{"id":"b","name":"B","device":"approved"},{"id":"c","name":"C","device":"approved","last_seen":"2026-10-20T11:59:00Z"}]}`, "nodes-seen", doctor.Warn, "2 of 3 approved nodes not heard from in 5m0s: A (a, 1h0m0s ago); B (b, never)", true},
		{"broken registry", ":8766", "127.0.0.1:8766", `{`, "nodes-seen", doctor.Warn, "does not read", true},
		{"no FrIDE", ":8766", "127.0.0.1:8766", `{"nodes":[{"id":"a","name":"A","device":"approved","last_seen":"2026-10-20T11:59:00Z"}]}`, "nodes-fride", doctor.OK, "no node offers FrIDE yet", true},
		{"FrIDE answers", ":8766", "127.0.0.1:8766", `{"nodes":[{"id":"a","name":"A","device":"approved","last_seen":"2026-10-20T11:59:00Z","declared":{"fride":{"url":"http://127.0.0.1:7777"}},"approved":{"fride":{"url":"http://127.0.0.1:7777"}}}]}`, "nodes-fride", doctor.OK, "FrIDE answers on A (http://127.0.0.1:7777)", true},
		{"FrIDE not logged in", ":8766", "127.0.0.1:8766", `{"nodes":[{"id":"a","name":"A","device":"approved","last_seen":"2026-10-20T11:59:00Z","declared":{"fride":{"url":"http://127.0.0.1:7777","not_logged_in":true}},"approved":{"fride":{"url":"http://127.0.0.1:7777"}}}]}`, "nodes-fride", doctor.Warn, "1 of 1 nodes with FrIDE cannot take work: A (a, FrIDE's Claude is not logged in)", true},
		{"FrIDE down", ":8766", "127.0.0.1:8766", `{"nodes":[{"id":"a","name":"A","device":"approved","last_seen":"2026-10-20T11:59:00Z","approved":{"fride":{"url":"http://127.0.0.1:7777"}}},{"id":"b","name":"B","device":"approved","last_seen":"2026-10-20T10:00:00Z","declared":{"fride":{"url":"http://127.0.0.1:7777"}},"approved":{"fride":{"url":"http://127.0.0.1:7777"}}}]}`, "nodes-fride", doctor.Warn, "2 of 2 nodes with FrIDE cannot take work: A (a, FrIDE does not answer); B (b, not heard from)", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := healthyMac()
			s.listening = map[string]bool{tc.listening: tc.listening != ""}
			if tc.nodesJSON != "" {
				s.contents = map[string]string{registry: tc.nodesJSON}
			}
			rep := doctor.Run(context.Background(), s.doctorEnv(on(tc.listen), nil), DoctorRegistry.Checks())
			got := result(t, rep, tc.id)
			if got.Status != tc.status || !strings.Contains(got.Detail, tc.detail) {
				t.Errorf("%s: %s %q, want %s with %q", tc.id, got.Status, got.Detail, tc.status, tc.detail)
			}
			if got.Status != doctor.OK && got.Fix == "" {
				t.Errorf("%s is %s without a fix", tc.id, got.Status)
			}
			if rep.OK != tc.ok {
				t.Errorf("report OK %v, want %v", rep.OK, tc.ok)
			}
		})
	}
}

// The node checks ask the running bridge first: a node connected now is
// heard from, whatever last_seen nodes.json still holds (it writes it
// once a minute). Without a bridge they read the file, where online
// means nothing.
func TestDoctorNodeChecksPreferTheBridge(t *testing.T) {
	t.Parallel()
	const registry = "/home/you/.fridolin/nodes/nodes.json"
	// The file kept the time of the connect, an hour before doctor runs.
	const file = `{"nodes":[{"id":"m","name":"mac-mini","device":"approved","last_seen":"2026-10-20T11:00:00Z","online":true,"declared":{"fride":{"url":"http://127.0.0.1:7777"}},"approved":{"fride":{"url":"http://127.0.0.1:7777"}}}]}`
	const live = `{"enabled":true,"listen":"127.0.0.1:8766","nodes":[{"id":"m","name":"mac-mini","device":"approved","last_seen":"2026-10-20T11:00:00Z","online":true,"declared":{"fride":{"url":"http://127.0.0.1:7777"}},"approved":{"fride":{"url":"http://127.0.0.1:7777"}},"commands":"approved","effective":{}}]}`
	cfg := doctorConfig(func(c *config.Settings) { c.Nodes = config.NodesConfig{Enabled: true, Listen: ":8766"} })
	answer := func(data string, err error) func(context.Context, *config.Settings) ([]byte, error) {
		return func(context.Context, *config.Settings) ([]byte, error) { return []byte(data), err }
	}
	tests := []struct {
		name                    string
		live                    func(context.Context, *config.Settings) ([]byte, error)
		status                  string
		seenDetail, frideDetail string
	}{
		{"bridge answers", answer(live, nil), doctor.OK, "1 approved nodes, each heard from", "FrIDE answers on mac-mini"},
		{"bridge down", answer("", errors.New("connection refused")), doctor.Warn, "mac-mini (m, 1h0m0s ago)", "mac-mini (m, not heard from)"},
		{"bridge answers garbage", answer("<html>", nil), doctor.Warn, "mac-mini (m, 1h0m0s ago)", "mac-mini (m, not heard from)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := healthyMac()
			s.listening = map[string]bool{"127.0.0.1:8766": true}
			s.contents = map[string]string{registry: file}
			env := s.doctorEnv(cfg, nil)
			env.LiveNodes = tc.live
			rep := doctor.Run(context.Background(), env, DoctorRegistry.Checks())
			if got := result(t, rep, "nodes-seen"); got.Status != tc.status || !strings.Contains(got.Detail, tc.seenDetail) {
				t.Errorf("nodes-seen: %s %q, want %s with %q", got.Status, got.Detail, tc.status, tc.seenDetail)
			}
			if got := result(t, rep, "nodes-fride"); got.Status != tc.status || !strings.Contains(got.Detail, tc.frideDetail) {
				t.Errorf("nodes-fride: %s %q, want %s with %q", got.Status, got.Detail, tc.status, tc.frideDetail)
			}
		})
	}
}

// askBridgeNodes asks GET /api/nodes with the dashboard token from the
// data dir, on loopback for a bridge bound to every interface.
func TestAskBridgeNodes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	token := config.Layout{Root: dir}.DashboardToken()
	if err := os.WriteFile(token, []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/nodes" || r.Header.Get("X-Bridge-Token") != "tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"nodes":[]}`))
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	cfg := &config.Settings{}
	cfg.HTTPEnabled, cfg.HTTPListen, cfg.DataDir = true, "0.0.0.0:"+port, dir
	if data, err := askBridgeNodes(context.Background(), cfg); err != nil || string(data) != `{"nodes":[]}` {
		t.Fatalf("got %q, %v", data, err)
	}
	if err := os.WriteFile(token, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := askBridgeNodes(context.Background(), cfg); err == nil {
		t.Error("a refused token reads as an answer")
	}
	cfg.HTTPEnabled = false
	if _, err := askBridgeNodes(context.Background(), cfg); err == nil {
		t.Error("asked a bridge whose HTTP server is off")
	}
}

// nodes-version warns about approved nodes older than the core, by the
// running bridge's version, and about failed updates.
func TestDoctorNodesVersion(t *testing.T) {
	t.Parallel()
	cfg := doctorConfig(func(c *config.Settings) { c.Nodes = config.NodesConfig{Enabled: true, Listen: ":8766"} })
	for _, tc := range []struct {
		name, live, status, detail string
	}{
		{"current", `{"version":"v1.2.0","nodes":[{"id":"a","name":"A","device":"approved","version":"v1.2.0"},{"id":"p","name":"P","device":"pending","version":"v0.1.0"}]}`, doctor.OK, "1 approved nodes run v1.2.0"},
		{"older", `{"version":"v1.2.0","nodes":[{"id":"a","name":"A","device":"approved","version":"v1.1.0"},{"id":"b","name":"B","device":"approved","version":"v1.3.0"}]}`, doctor.Warn, "1 of 2 approved nodes run an older or other fridolin than the core's v1.2.0: A (a, v1.1.0)"},
		{"same commit, other abbreviation", `{"version":"06ab6d82","nodes":[{"id":"a","name":"A","device":"approved","version":"dev-06ab6d8"},{"id":"b","name":"B","device":"approved","version":"06ab6d8"}]}`, doctor.OK, "2 approved nodes run 06ab6d82"},
		{"failed", `{"version":"v1.2.0","nodes":[{"id":"a","name":"A","device":"approved","version":"v1.2.0","update":{"state":"failed","to":"v1.2.0","error":"no luck"}}]}`, doctor.Warn, "the last update failed on A (a, to v1.2.0: no luck)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := healthyMac()
			s.listening = map[string]bool{"127.0.0.1:8766": true}
			env := s.doctorEnv(cfg, nil)
			env.LiveNodes = func(context.Context, *config.Settings) ([]byte, error) { return []byte(tc.live), nil }
			got := result(t, doctor.Run(context.Background(), env, DoctorRegistry.Checks()), "nodes-version")
			if got.Status != tc.status || !strings.Contains(got.Detail, tc.detail) {
				t.Errorf("nodes-version: %s %q, want %s with %q", got.Status, got.Detail, tc.status, tc.detail)
			}
		})
	}
}
