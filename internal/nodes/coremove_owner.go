package nodes

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// What the owner's !movecore needs of the fleet (#187 §3, §5): which
// core host runs the core now, a node by the name the owner types, the
// site as a move carries it, and forgetting the host secrets on the
// machine a move left behind.

// MoveSite is the site as the core runs it, for a move's report: whether
// it is on, its domain, and whether its Cloudflare tunnel runs inside
// the core (site.tunnel), which moves with the core.
type MoveSite struct {
	Enabled       bool   `json:"enabled"`
	Domain        string `json:"domain,omitempty"`
	Tunnel        bool   `json:"tunnel"`
	TunnelRunning bool   `json:"tunnel_running"`
	// After says, in words, what becomes of the site once the core runs
	// on the target (MoveCheck fills it).
	After string `json:"after,omitempty"`
}

// tunnelOnHost is how the source's machine-bound scan names a
// Cloudflare tunnel run on the host (nodeagent/corescan.go).
const tunnelOnHost = "Cloudflare tunnel runs on this machine"

// moveSite fills rep.Site from what the core runs, and says what the
// site does after the move.
func (s *Service) moveSite(rep *MoveReport) {
	if s.site == nil {
		return
	}
	site := s.site()
	if site == nil || !site.Enabled {
		return
	}
	where := "the site"
	if site.Domain != "" {
		where = site.Domain
	}
	hostTunnel := false
	for _, m := range rep.MachineBound {
		hostTunnel = hostTunnel || strings.Contains(m, tunnelOnHost)
	}
	switch {
	case site.Tunnel && site.TunnelRunning:
		// The core's own tunnel keeps the site up after the move, so a
		// cloudflared job on the host is not something it loses (#248).
		rep.MachineBound = slices.DeleteFunc(rep.MachineBound, func(m string) bool { return strings.Contains(m, tunnelOnHost) })
		site.After = fmt.Sprintf("the site's tunnel runs inside the core and moves with it: %s answers from %s once the core runs there", where, rep.Target.Name)
	case site.Tunnel:
		site.After = fmt.Sprintf("the site's tunnel runs inside the core and moves with it, but it is not running now: %s does not answer before the move either (Site page)", where)
		rep.Warnings = append(rep.Warnings, "the site's tunnel is turned on but not running: "+where+" does not answer now")
	case hostTunnel:
		site.After = fmt.Sprintf("the site's tunnel runs on %s, not in the core: %s goes offline once the core moves; turn on Site → Run the tunnel before moving", rep.Source.Name, where)
	default:
		site.After = fmt.Sprintf("the site runs without a tunnel in the core: it moves to %s, and only what reaches the core there reaches it", rep.Target.Name)
	}
	rep.Site = site
}

// CoreHostState is a core host as !movecore lists it.
type CoreHostState struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	OS     string `json:"os,omitempty"`
	Online bool   `json:"online"`
	// Approved is an approved device that offers core-host; Confirmed
	// one the owner confirmed (a move to it once had their vote).
	Approved  bool `json:"approved"`
	Confirmed bool `json:"confirmed"`
	// RunsCore is set for the host whose core holds the lease.
	RunsCore bool `json:"runs_core"`
	// Problem says why it could not be asked.
	Problem string `json:"problem,omitempty"`
}

// CoreHosts lists the nodes that offer core-host, and
// asks each approved one that is online whether it runs the core
// (core.check: its data dir holds the lease).
func (s *Service) CoreHosts(ctx context.Context) ([]CoreHostState, error) {
	all, err := s.reg.List()
	if err != nil {
		return nil, err
	}
	var out []CoreHostState
	for _, n := range all {
		if n.Device == nodeproto.DeviceRevoked || n.Declared.CoreHost == nil {
			continue
		}
		st := CoreHostState{ID: n.ID, Name: n.Name, OS: n.OS, Online: s.Online(n.ID), Confirmed: n.CoreHostConfirmed(),
			Approved: n.Device == nodeproto.DeviceApproved && n.Effective().CoreHost != nil}
		if st.Approved && st.Online {
			var c nodeproto.CoreCheckResult
			cctx, cancel := context.WithTimeout(ctx, time.Minute)
			err := s.Call(cctx, n.ID, nodeproto.MethodCoreCheck, nil, &c)
			cancel()
			// A core that cannot be read (a host that never ran this
			// version) runs nothing: MoveCheck says whether that matters.
			switch {
			case err != nil:
				st.Problem = err.Error()
			case c.Core != nil:
				st.RunsCore = c.Core.HeldBy()
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// FindNode is the node the owner names: by its id, or by its name when
// exactly one node that is not revoked has it (any case).
func (s *Service) FindNode(nameOrID string) (*Node, error) {
	if n, err := s.reg.Get(nameOrID); err == nil {
		return n, nil
	}
	all, err := s.reg.List()
	if err != nil {
		return nil, err
	}
	var found *Node
	for _, n := range all {
		if n.Device == nodeproto.DeviceRevoked || !strings.EqualFold(n.Name, nameOrID) {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("%w: two nodes are called %s: name it by its id", ErrInvalidInput, nameOrID)
		}
		found = n
	}
	if found == nil {
		return nil, fmt.Errorf("%w %q", ErrUnknownNode, nameOrID)
	}
	return found, nil
}

// CoreForget is what asking a core host to forget what moves brought it
// did (#266): the host secrets it removed (Forgotten), the secret files
// (Files, ~/dir/name) and the entries of the core's data dir (Data);
// DataKept says why its data dir stays, and Error what could not be
// removed, or why it could not be asked.
type CoreForget struct {
	Forgotten []string `json:"forgotten"`
	Files     []string `json:"files,omitempty"`
	Data      []string `json:"data,omitempty"`
	DataKept  string   `json:"data_kept,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// ForgetCoreSecrets asks core host id to remove what moves brought it
// over the node channel (core.secrets.forget): the host secrets, the
// secret files they wrote and the core's data dir its import put into an
// empty one. What the machine held before stays: secrets typed there, its
// own files, the data dir of the core's original home.
func (s *Service) ForgetCoreSecrets(ctx context.Context, id string) CoreForget {
	if !s.Online(id) {
		return CoreForget{Forgotten: []string{}, Error: "it is offline"}
	}
	var res nodeproto.CoreSecretsForgotten
	if err := s.Call(ctx, id, nodeproto.MethodCoreSecretsForget, struct{}{}, &res); err != nil {
		s.logf("nodes: %s forgetting what moves brought it: %v", id, err)
		return CoreForget{Forgotten: []string{}, Error: err.Error()}
	}
	s.logf("nodes: %s forgot %d host secrets, %d secret files and %d entries of the core's data dir", id, len(res.Forgotten), len(res.Files), len(res.Data))
	return CoreForget{Forgotten: append([]string{}, res.Forgotten...), Files: res.Files, Data: res.Data, DataKept: res.DataKept, Error: strings.Join(res.Problems, "; ")}
}
