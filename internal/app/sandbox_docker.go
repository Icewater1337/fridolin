package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/google"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/sandboxrun"
)

// markerFile is the file in a brain's folder that holds the runtime id of
// the core that runs the brain in a Docker Sandbox (brainRuntimes.marker).
const markerFile = ".fridolin-runtime-id"

// sandboxMemoryMB is each brain sandbox's memory cap.
const sandboxMemoryMB = 4096

// writeMarker writes marker into dir's markerFile, through the folder:
// a brain cannot point it elsewhere with a symlink.
func writeMarker(dir, marker string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if old, err := root.ReadFile(markerFile); err == nil && string(old) == marker+"\n" {
		return nil
	}
	return root.WriteFile(markerFile, []byte(marker+"\n"), 0o644)
}

// errSandboxUnavailable is a Docker Sandbox that is not available for a
// run, which then falls back (or is refused with fallback "refuse").
var errSandboxUnavailable = errors.New("the Docker Sandbox is not available")

// ensure has the node make spec's sandbox ready. An unavailable node or
// a marker that is not this core's is errSandboxUnavailable; a sandbox
// whose setup failed refuses the run.
func (r *brainRuntimes) ensure(ctx context.Context, spec brain.RunSpec, c runtimeChoice) error {
	if err := writeMarker(spec.Dir, r.marker); err != nil {
		return fmt.Errorf("%w: writing %s: %v", errSandboxUnavailable, markerFile, err)
	}
	cfg := r.b.cfgs.Get()
	p := nodeproto.SandboxEnsureParams{Brain: spec.ID, Folder: spec.Dir, Marker: r.marker, MemoryMB: sandboxMemoryMB,
		Egress: c.Egress, APIPort: r.apiPort(), Provider: spec.Settings.Provider}
	for _, a := range cfg.Sandbox.AllowPaths {
		p.Mounts = append(p.Mounts, nodeproto.SandboxMount{Path: AbsPath(a), ReadOnly: true})
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var res nodeproto.SandboxEnsureResult
	err := r.b.sandboxFleet().Call(ctx, c.state.Node, nodeproto.MethodSandboxEnsure, p, &res)
	var ne *nodeproto.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ne) && ne.Code == nodeproto.CodeFailed:
		return &brain.RunRefused{Reason: "the Docker Sandbox could not be set up: " + err.Error(), Notice: outboundmsg.SandboxSetupFailedNotice}
	}
	return fmt.Errorf("%w: %v", errSandboxUnavailable, err)
}

// vmAccess is access as a run in the VM uses it. The core builds the MCP
// config, inline (no file of the core's is in the VM), with the VM's
// fridolin that sandbox.info names; the node does not rewrite it. The
// server takes FRIDOLIN_API and FRIDOLIN_BRAIN_TOKEN_FILE from the run's
// environment, where the node puts them, so the config names neither.
func vmAccess(access runAccess, info nodeproto.SandboxInfo) runAccess {
	if access.RunID == "" || info.BinPath == "" {
		return runAccess{}
	}
	cmd := []string{info.BinPath, "mcp", "--run", access.RunID}
	cfg, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		mcpServerName: map[string]any{"type": "stdio", "command": cmd[0], "args": cmd[1:]},
	}})
	if err != nil {
		return runAccess{}
	}
	return runAccess{MCPConfig: string(cfg), MCPCommand: cmd}
}

// vmEnv is a run's environment in the VM: what it needs and holds no
// secret. The node adds the provider's login, FRIDOLIN_API and
// FRIDOLIN_BRAIN_TOKEN_FILE; the run token goes only in sandbox.run's
// token.
func vmEnv(spec brain.RunSpec, info nodeproto.SandboxInfo) map[string]string {
	s := spec.Settings
	env := map[string]string{"FRIDOLIN_BRAIN_ID": spec.ID, "DISABLE_AUTOUPDATER": "1"}
	kvs := append(google.CalendarEnv(spec.ID, s.Manifest, ""), plaudOptOut...)
	if s.Zone != nil {
		kvs = append(kvs, zoneEnv(s.Zone.String())...)
	}
	for _, kv := range kvs {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	if info.BinPath != "" {
		env["FRIDOLIN_BIN"] = info.BinPath
	}
	if info.ToolsPath != "" {
		env["FRIDOLIN_TOOLS_DIR"] = info.ToolsPath
	}
	return env
}

// forRun is where spec's run happens. A run of a brain set to Docker
// Sandboxes whose sandbox is not available falls back to the bridge's
// own sandbox, or with fallback "refuse" is refused; one whose sandbox
// cannot be set up is refused. A run never changes runtime once started.
func (r *brainRuntimes) forRun(ctx context.Context, spec brain.RunSpec) (runtimeChoice, error) {
	s := spec.Settings
	c := r.choose(ctx, s.Manifest.Sandbox, s.Provider, true)
	if c.Effective == manifest.RuntimeDockerSandbox {
		err := r.ensure(ctx, spec, c)
		if refused, ok := errors.AsType[*brain.RunRefused](err); ok {
			log.Printf("brain %s: %s", spec.ID, refused.Reason)
			c.Effective = effectiveRefused
			return c, refused
		}
		if err != nil {
			c.Ready, c.Reason = false, err.Error()
			c.Effective = localRuntime(r.b.cfgs.Get())
			if c.Fallback == manifest.FallbackRefuse {
				c.Effective = effectiveRefused
			}
		}
	}
	switch {
	case c.Effective == effectiveRefused:
		log.Printf("brain %s: refusing the run, its Docker Sandbox is not available: %s", spec.ID, c.Reason)
		return c, &brain.RunRefused{Reason: "its Docker Sandbox is not available: " + c.Reason, Notice: outboundmsg.SandboxRefusedNotice}
	case c.Configured == manifest.RuntimeDockerSandbox && c.Effective != manifest.RuntimeDockerSandbox:
		log.Printf("brain %s: its Docker Sandbox is not available (%s), running under %s", spec.ID, c.Reason, c.Effective)
	}
	return c, nil
}

// launcher starts a run in spec's Docker Sandbox: Spawn.Launch.
func (r *brainRuntimes) launcher(spec brain.RunSpec, c runtimeChoice, access runAccess, timeout time.Duration) func(context.Context, claudecli.Spawn) (claudecli.Process, error) {
	node, fleet := c.state.Node, r.b.sandboxFleet()
	return func(ctx context.Context, s claudecli.Spawn) (claudecli.Process, error) {
		p := nodeproto.SandboxRunParams{Brain: spec.ID, Run: access.RunID, Folder: spec.Dir, Argv: append([]string{s.Binary}, s.Args...),
			Env: vmEnv(spec, c.state.Info), Stdin: s.Prompt, Token: access.Token, APIPort: r.apiPort(), Provider: spec.Settings.Provider, TimeoutS: int(timeout / time.Second)}
		if p.Run == "" {
			p.Run = "r" + randomToken(9)
		}
		call := func(ctx context.Context, method string, params, out any) error {
			return fleet.Call(ctx, node, method, params, out)
		}
		return sandboxrun.Start(ctx, call, p, s.Stdout, sandboxrun.Options{Label: "sandbox run " + p.Run + " on " + c.Node})
	}
}

// apiPort is the port of the core's API, which the VM reaches on the
// Mac's loopback; 0 without one.
func (r *brainRuntimes) apiPort() int {
	_, port, err := net.SplitHostPort(r.b.listen)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(port)
	return n
}
