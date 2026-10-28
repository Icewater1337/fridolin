package nodes

import (
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// A fride_host node declares again whenever its free memory changes:
// only a change in what it offers is news for the log.
func TestOffersChanged(t *testing.T) {
	t.Parallel()
	host := func(avail int, instances ...string) *Node {
		h := &nodeproto.FrideHostCap{Version: "v0.50.0", Ready: true, TotalMemoryMB: 24576, AvailableMemoryMB: avail, ReserveMB: 3072}
		for _, name := range instances {
			h.Instances = append(h.Instances, nodeproto.FrideInstance{Name: name, State: nodeproto.FrideInstanceRunning, MemoryMB: 3072})
		}
		return &Node{Declared: nodeproto.Capabilities{Host: true, FrideHost: h}}
	}
	if !offersChanged(nil, host(8000)) {
		t.Error("a first declaration is no news")
	}
	if offersChanged(host(8000), host(7000)) {
		t.Error("free memory alone is news")
	}
	if !offersChanged(host(8000), host(8000, "work")) {
		t.Error("a new instance is no news")
	}
	if !offersChanged(host(8000), &Node{Declared: nodeproto.Capabilities{Host: true}}) {
		t.Error("fride_host gone is no news")
	}
}
