package nodeagent

import (
	"context"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// SandboxStatus is how Docker Sandboxes stand on this machine, for
// `fridolin doctor --node` and `fridolin node sandbox status`.
type SandboxStatus struct {
	// Sbx is sbx's path, "" where it is not installed.
	Sbx string
	// Off is the capability turned off here.
	Off bool
	// Info is what sbx says, as sandbox.info would; Err is why there is
	// none (sbx does not run).
	Info nodeproto.SandboxInfo
	Err  error
	// ClaudeToken is the Claude token in the host's secret store.
	ClaudeToken bool
}

// SetUp reports whether `fridolin node sandbox setup` recorded a
// template: only then does the node declare sandbox.
func (s SandboxStatus) SetUp() bool { return s.Err == nil && s.Info.Template != "" }

// LookAtSandbox probes sbx at path for the node with state dir dir,
// changing nothing; secrets is the host's store (nil for none).
func LookAtSandbox(ctx context.Context, dir, sbx string, secrets hostsecrets.Store) SandboxStatus {
	s := SandboxStatus{Sbx: sbx, Off: SandboxOff(dir)}
	a := &Agent{Dir: dir, SandboxSbx: sbx, CoreSecrets: secrets, Logf: func(string, ...any) {}, sandboxLookOnly: true}
	s.ClaudeToken = a.claudeToken() != ""
	if sbx == "" || s.Off {
		return s
	}
	s.Info, s.Err = a.sandboxProbe(ctx)
	return s
}
