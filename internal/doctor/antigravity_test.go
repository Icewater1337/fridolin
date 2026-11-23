package doctor

import (
	"context"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// agy is checked when a brain runs on it, by agent_provider or by its own
// brain.json, and found in ~/.local/bin, where its installer puts it.
func TestAntigravityCheck(t *testing.T) {
	brainOnAgy := func(c *config.Settings) {
		c.AntigravityPath = "agy"
		c.BrainDirs = []config.BrainRef{{Path: "/home/you/b", Approved: &manifest.Manifest{ID: "b", Provider: manifest.ProviderAntigravity}}}
	}
	for _, tc := range []struct {
		name   string
		mut    func(*config.Settings)
		path   map[string]string
		status string
		ran    bool
	}{
		{"no brain on agy", func(c *config.Settings) { c.AntigravityPath = "agy" }, nil, OK, false},
		{"agent_provider", func(c *config.Settings) { c.AgentProvider, c.AntigravityPath = "antigravity", "agy" }, map[string]string{"agy": "/usr/local/bin/agy"}, OK, true},
		{"one brain, in ~/.local/bin", brainOnAgy, map[string]string{"/home/you/.local/bin/agy": "/home/you/.local/bin/agy"}, OK, true},
		{"one brain, missing", brainOnAgy, nil, Fail, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := healthyMac()
			for k, v := range tc.path {
				s.path[k] = v
				s.runs[v+" --version"] = fakeRun{out: "1.5.14\n"}
			}
			rep := Run(context.Background(), s.doctorEnv(testConfig(tc.mut), nil), allChecks())
			var got *CheckReport
			for i, c := range rep.Checks {
				if c.ID == "antigravity" {
					got = &rep.Checks[i]
				}
			}
			if (got != nil) != tc.ran {
				t.Fatalf("check ran: %v, want %v", got != nil, tc.ran)
			}
			if got != nil && (got.Status != tc.status || tc.status == OK && !strings.Contains(got.Detail, "1.5.14")) {
				t.Errorf("check = %+v, want %s", got, tc.status)
			}
		})
	}
}
