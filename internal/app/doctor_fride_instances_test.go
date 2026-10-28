package app

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/doctor"
)

// The FrIDE instances check: each instance needs an approved, ready node
// it may run on with room for its smallest start, and the identities it
// names; without instances it does not apply.
func TestDoctorFrideInstances(t *testing.T) {
	t.Parallel()
	const registry = "/home/you/.fridolin/nodes/nodes.json"
	const gitlabFile = "/home/you/.fridolin/core/local-secrets/gitlab.json"
	host := func(id, name, extra string) string {
		return `{"id":"` + id + `","name":"` + name + `","device":"approved","last_seen":"2026-10-20T11:59:00Z","declared":{"fride_host":{"version":"1","ready":true,"total_memory_mb":32768,"available_memory_mb":20000,"reserve_mb":6144,"problem":"` + extra + `"}},"approved":{"fride_host":{"version":"1","ready":true,"total_memory_mb":0,"available_memory_mb":0,"reserve_mb":0}}}`
	}
	notReady := `{"id":"b","name":"box","device":"approved","last_seen":"2026-10-20T11:59:00Z","declared":{"fride_host":{"version":"1","ready":false,"problem":"sbx is not signed in","total_memory_mb":32768,"available_memory_mb":20000,"reserve_mb":6144}},"approved":{"fride_host":{"version":"1","ready":false,"total_memory_mb":0,"available_memory_mb":0,"reserve_mb":0}}}`
	instances := map[string]config.FrideInstanceConfig{
		"work":     {Identities: []string{"gitlab"}, Nodes: []string{"mini"}},
		"fridolin": {Identities: []string{"github"}},
	}
	needsTemplate := strings.Replace(host("a", "mini", ""), `"reserve_mb":6144,`, `"reserve_mb":6144,"needs_template":true,`, 1)
	tests := []struct {
		name, nodes string
		live        string
		gitlab      bool
		registry    string
		status      string
		detail      string
	}{
		{"no nodes", "", "", true, "", doctor.Warn, "2 of 2 FrIDE instances cannot run: fridolin: no approved node offers FrIDE instances (fride_host); work: no approved node offers FrIDE instances (fride_host) among mini"},
		{"ready, no bridge", `{"nodes":[` + host("a", "mini", "") + `]}`, "", true, "", doctor.OK, "each can start on an approved node, with its identities: fridolin, work"},
		{"only elsewhere", `{"nodes":[` + host("a", "laptop", "") + `]}`, "", true, "", doctor.Warn, "1 of 2 FrIDE instances cannot run: work: no approved node offers FrIDE instances (fride_host) among mini"},
		{"not ready", `{"nodes":[` + notReady + `]}`, "", true, "", doctor.Warn, "fridolin: no node can run it (box: sbx is not signed in)"},
		{"no GitLab", `{"nodes":[` + host("a", "mini", "") + `]}`, "", false, "", doctor.Warn, "1 of 2 FrIDE instances cannot run: work: the core holds no gitlab identity"},
		{"no GitHub, as the bridge says", `{"nodes":[` + host("a", "mini", "") + `]}`, `{"identities":[{"id":"github","set":false},{"id":"gitlab","set":true}]}`, false, "", doctor.Warn, "1 of 2 FrIDE instances cannot run: fridolin: the core holds no github identity"},
		{"no template for a node that built none", `{"nodes":[` + needsTemplate + `]}`, "", true, "", doctor.Warn, "fridolin: no node can run it (mini: it built no FrIDE template, and this core does not know where its images are published (FRIDOLIN_BOX_REGISTRY)"},
		{"the core's template", `{"nodes":[` + needsTemplate + `]}`, "", true, "ghcr.io/you", doctor.OK, "each can start on an approved node, with its identities: fridolin, work"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := healthyMac()
			s.listening = map[string]bool{"127.0.0.1:8766": true}
			if tc.nodes != "" {
				s.contents = map[string]string{registry: tc.nodes}
			}
			if tc.gitlab {
				s.files = map[string]fs.FileMode{gitlabFile: 0o600}
			}
			if tc.registry != "" {
				s.env = maps.Clone(s.env)
				if s.env == nil {
					s.env = map[string]string{}
				}
				s.env["FRIDOLIN_BOX_REGISTRY"] = tc.registry
			}
			cfg := doctorConfig(func(c *config.Settings) {
				c.Nodes = config.NodesConfig{Enabled: true, Listen: ":8766"}
				c.Fride.Instances = instances
			})
			env := s.doctorEnv(cfg, nil)
			env.LiveGet = func(_ context.Context, _ *config.Settings, path string) ([]byte, error) {
				if tc.live == "" || path != "/api/git-identities" {
					return nil, errors.New("connection refused")
				}
				return []byte(tc.live), nil
			}
			got := result(t, doctor.Run(context.Background(), env, DoctorRegistry.Checks()), "fride-instances")
			if got.Status != tc.status || !strings.Contains(got.Detail, tc.detail) {
				t.Errorf("%s %q, want %s with %q", got.Status, got.Detail, tc.status, tc.detail)
			}
			if got.Status != doctor.OK && got.Fix == "" {
				t.Error("a warning without a fix")
			}
		})
	}
	rep := doctor.Run(context.Background(), healthyMac().doctorEnv(doctorConfig(func(c *config.Settings) { c.Nodes.Enabled = true }), nil), DoctorRegistry.Checks())
	for _, c := range rep.Checks {
		if c.ID == "fride-instances" {
			t.Error("the check runs with no instances")
		}
	}
}
