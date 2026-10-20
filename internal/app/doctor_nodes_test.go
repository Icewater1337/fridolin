package app

import (
	"context"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
)

// The node checks apply with nodes.enabled: the port must answer (M2's
// gate), and approved nodes not heard from for 5 minutes are a warning.
func TestDoctorNodeChecks(t *testing.T) {
	off := RunDoctor(context.Background(), healthyMac().doctorEnv(doctorConfig(nil), nil), DoctorRegistry)
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
		{"port up", ":8766", "127.0.0.1:8766", "", "nodes-port", DoctorOK, "answers on 127.0.0.1:8766", true},
		{"port up on an address", "100.64.0.1:9000", "100.64.0.1:9000", "", "nodes-port", DoctorOK, "100.64.0.1:9000", true},
		{"port down", ":8766", "", "", "nodes-port", DoctorFail, "nothing answers on 127.0.0.1:8766", false},
		{"no registry", ":8766", "127.0.0.1:8766", "", "nodes-seen", DoctorOK, "no nodes yet", true},
		{"only pending", ":8766", "127.0.0.1:8766", `{"nodes":[{"id":"a","name":"A","device":"pending"}]}`, "nodes-seen", DoctorOK, "no approved nodes", true},
		{"fresh", ":8766", "127.0.0.1:8766", `{"nodes":[{"id":"a","name":"A","device":"approved","last_seen":"2026-10-20T11:58:00Z"},{"id":"r","name":"R","device":"revoked","last_seen":"2026-01-01T00:00:00Z"}]}`, "nodes-seen", DoctorOK, "1 approved nodes, each heard from", true},
		{"stale", ":8766", "127.0.0.1:8766", `{"nodes":[{"id":"a","name":"A","device":"approved","last_seen":"2026-10-20T11:00:00Z"},{"id":"b","name":"B","device":"approved"},{"id":"c","name":"C","device":"approved","last_seen":"2026-10-20T11:59:00Z"}]}`, "nodes-seen", DoctorWarn, "2 of 3 approved nodes not heard from in 5m0s: A (a, 1h0m0s ago); B (b, never)", true},
		{"broken registry", ":8766", "127.0.0.1:8766", `{`, "nodes-seen", DoctorWarn, "does not read", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := healthyMac()
			s.listening = map[string]bool{tc.listening: tc.listening != ""}
			if tc.nodesJSON != "" {
				s.contents = map[string]string{registry: tc.nodesJSON}
			}
			rep := RunDoctor(context.Background(), s.doctorEnv(on(tc.listen), nil), DoctorRegistry)
			got := result(t, rep, tc.id)
			if got.Status != tc.status || !strings.Contains(got.Detail, tc.detail) {
				t.Errorf("%s: %s %q, want %s with %q", tc.id, got.Status, got.Detail, tc.status, tc.detail)
			}
			if got.Status != DoctorOK && got.Fix == "" {
				t.Errorf("%s is %s without a fix", tc.id, got.Status)
			}
			if rep.OK != tc.ok {
				t.Errorf("report OK %v, want %v", rep.OK, tc.ok)
			}
		})
	}
}
