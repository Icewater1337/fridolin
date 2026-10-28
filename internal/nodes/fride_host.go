package nodes

import (
	"context"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// The fride_host calls: FrIDE instances, each a whole FrIDE in a Docker
// Sandbox of its own on a node (docs/NODES.md, "FrIDE instances").

// on names instance for a fride call ("" for the node's own FrIDE).
func on(instance string) nodeproto.FrideOn { return nodeproto.FrideOn{Instance: instance} }

// FrideMemOn asks instance's FrIDE on node id whether a stack may start
// now.
func (s *Service) FrideMemOn(ctx context.Context, id, instance string) (nodeproto.FrideMem, error) {
	var out nodeproto.FrideMem
	return out, s.Call(ctx, id, nodeproto.MethodFrideMem, on(instance), &out)
}

// FrideInstanceStart starts an instance on node id, or brings a running
// one's repositories, identities and egress up to date (its memory
// changed: remade at the new size). It answers once its FrIDE answers and
// is paired.
func (s *Service) FrideInstanceStart(ctx context.Context, id string, p nodeproto.FrideInstanceStartParams) (nodeproto.FrideInstance, error) {
	var out nodeproto.FrideInstance
	return out, s.Call(ctx, id, nodeproto.MethodFrideInstanceStart, p, &out)
}

// FrideInstanceStop stops instance's VM on node id; its disk stays.
func (s *Service) FrideInstanceStop(ctx context.Context, id, instance string) (nodeproto.FrideInstance, error) {
	var out nodeproto.FrideInstance
	return out, s.Call(ctx, id, nodeproto.MethodFrideInstanceStop, nodeproto.FrideInstanceRef{Name: instance}, &out)
}

// FrideInstanceRemove saves instance's history on node id and removes
// its sandbox.
func (s *Service) FrideInstanceRemove(ctx context.Context, id, instance string) error {
	return s.Call(ctx, id, nodeproto.MethodFrideInstanceRemove, nodeproto.FrideInstanceRef{Name: instance}, nil)
}

// FrideInstanceStatus lists the instances node id has.
func (s *Service) FrideInstanceStatus(ctx context.Context, id string) (nodeproto.FrideInstances, error) {
	var out nodeproto.FrideInstances
	return out, s.Call(ctx, id, nodeproto.MethodFrideInstanceStatus, struct{}{}, &out)
}
