package app

import (
	"context"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

// sandboxFleet is how the core reaches the nodes with Docker Sandboxes:
// the nodes service, or a fake node in tests (testHooks.sandboxFleet).
type sandboxFleet interface {
	// SandboxNodes are the approved nodes whose sandbox capability is
	// approved, with whether each is connected.
	SandboxNodes() []sandboxNode
	// Call calls method on node id, as nodes.Service.Call does.
	Call(ctx context.Context, id, method string, params, out any) error
}

// sandboxNode is a node that offers Docker Sandboxes.
type sandboxNode struct {
	ID, Name string
	Online   bool
}

// nodesFleet is sandboxFleet over the nodes service.
type nodesFleet struct{ s *nodes.Service }

func (f nodesFleet) SandboxNodes() []sandboxNode {
	views, _ := f.s.Views()
	var out []sandboxNode
	for _, v := range views {
		if v.Device == nodeproto.DeviceApproved && v.Effective.Sandbox != nil {
			out = append(out, sandboxNode{ID: v.ID, Name: v.Name, Online: v.Online})
		}
	}
	return out
}

func (f nodesFleet) Call(ctx context.Context, id, method string, params, out any) error {
	return f.s.Call(ctx, id, method, params, out)
}

// sandboxFleet is the fleet the bridge runs brains on, nil without nodes.
func (b *App) sandboxFleet() sandboxFleet {
	if b.hooks.sandboxFleet != nil {
		return b.hooks.sandboxFleet
	}
	if b.nodes == nil {
		return nil
	}
	return nodesFleet{b.nodes}
}
