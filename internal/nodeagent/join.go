package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// JoinOptions are what `fridolin node join` was given.
type JoinOptions struct {
	// Dir is the node's state dir.
	Dir string
	// Core is the core's node port, host:port.
	Core string
	// Code is the single-use code of `fridolin nodes join-code`.
	Code         string
	Name         string
	Capabilities nodeproto.Capabilities
	// AndroidBuild is node.json's android_build: false keeps the node from
	// offering android-build where it finds a JDK and the Android SDK
	// (tests whose nodes must not depend on the machine's tools); nil
	// offers it then.
	AndroidBuild *bool
	Version      string
	Logf         func(string, ...any)
}

// Joined is what joining did.
type Joined struct {
	ID    string
	State nodeproto.State
}

// Join makes the node a new device of the core: a new key, node.json, and
// one connection that hands the core the join code. The device then waits
// for the owner's approval on the core, which the agent (Run) hears.
func Join(ctx context.Context, o JoinOptions) (Joined, error) {
	if o.Code == "" {
		return Joined{}, errors.New("the join code is missing: run fridolin nodes join-code on the core")
	}
	cfg := Config{Core: o.Core, Name: o.Name, Capabilities: o.Capabilities, AndroidBuild: o.AndroidBuild}
	if err := cfg.Check(); err != nil {
		return Joined{}, err
	}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return Joined{}, err
	}
	key, err := NewKey(o.Dir)
	if err != nil {
		return Joined{}, fmt.Errorf("making the device key: %w", err)
	}
	if err := SaveConfig(o.Dir, cfg); err != nil {
		return Joined{}, err
	}
	a := &Agent{Dir: o.Dir, Config: cfg, Key: key, Version: o.Version, Logf: o.Logf}
	c, st, err := a.connect(ctx, o.Code)
	if err != nil {
		return Joined{ID: a.ID()}, err
	}
	_ = c.send(ctx, nodeproto.TypeClose, "", nodeproto.Close{Reason: nodeproto.CloseShutdown, Message: "joined"})
	_ = c.ws.CloseNow()
	return Joined{ID: a.ID(), State: st}, nil
}
