package nodes

import (
	"slices"
	"strings"
	"testing"
)

// TestMoveSite: the move's report says what becomes of the site: a
// tunnel inside the core moves with it, one on the source's host does
// not, and a tunnel that is on but not running is a warning.
func TestMoveSite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		site    *MoveSite
		bound   []string
		after   string
		warning bool
	}{
		{"off", &MoveSite{}, nil, "", false},
		{"tunnel in the core", &MoveSite{Enabled: true, Domain: "example.com", Tunnel: true, TunnelRunning: true}, nil, "moves with it: example.com answers from icebase", false},
		{"tunnel in the core, a cloudflared job on the host", &MoveSite{Enabled: true, Domain: "example.com", Tunnel: true, TunnelRunning: true}, []string{"a Cloudflare tunnel runs on this machine (cloudflared.plist): the site goes offline", "Apple-only work stays on this Mac"}, "moves with it: example.com answers from icebase", false},
		{"tunnel not running", &MoveSite{Enabled: true, Domain: "example.com", Tunnel: true}, nil, "not running now", true},
		{"tunnel on the host", &MoveSite{Enabled: true, Domain: "example.com"}, []string{"a Cloudflare tunnel runs on this machine (cloudflared.plist): the site goes offline"}, "runs on mini, not in the core: example.com goes offline", false},
		{"no tunnel", &MoveSite{Enabled: true}, nil, "without a tunnel in the core", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{site: func() *MoveSite { cp := *tc.site; return &cp }}
			rep := MoveReport{Source: MoveEnd{Name: "mini"}, Target: MoveEnd{Name: "icebase"}, MachineBound: tc.bound, Warnings: []string{}}
			s.moveSite(&rep)
			switch {
			case tc.after == "" && rep.Site != nil:
				t.Errorf("a site that is off: %+v", rep.Site)
			case tc.after != "" && (rep.Site == nil || !strings.Contains(rep.Site.After, tc.after)):
				t.Errorf("after the move: %+v, want %q", rep.Site, tc.after)
			}
			// The host's tunnel is only lost when the core runs none (#248).
			hostLine := slices.ContainsFunc(rep.MachineBound, func(m string) bool { return strings.Contains(m, tunnelOnHost) })
			if want := slices.ContainsFunc(tc.bound, func(m string) bool { return strings.Contains(m, tunnelOnHost) }) && !tc.site.TunnelRunning; hostLine != want {
				t.Errorf("machine-bound: %q", rep.MachineBound)
			}
			if len(rep.MachineBound) == 0 && len(tc.bound) > 1 {
				t.Errorf("dropped more than the tunnel: %q", rep.MachineBound)
			}
			if got := len(rep.Warnings) > 0; got != tc.warning {
				t.Errorf("warnings: %q", rep.Warnings)
			}
		})
	}
}
