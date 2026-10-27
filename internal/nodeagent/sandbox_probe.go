package nodeagent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// sandboxProbeTimeout bounds one look at sbx.
const sandboxProbeTimeout = 30 * time.Second

// errNoSbx is a node without sbx, or with the capability turned off.
var errNoSbx = errors.New("sbx is not installed here, or sandbox is turned off")

// sandboxProbe asks sbx how it is: what sandbox.info answers. An error
// means sbx does not run at all (not found, or `sbx version` fails).
func (a *Agent) sandboxProbe(ctx context.Context) (nodeproto.SandboxInfo, error) {
	var info nodeproto.SandboxInfo
	d := a.sbx()
	if d == nil || !a.sandboxOffered() {
		return info, errNoSbx
	}
	ctx, cancel := context.WithTimeout(ctx, sandboxProbeTimeout)
	defer cancel()
	out, stderr, err := d.run(ctx, nil, nil, "version")
	if err != nil {
		return info, sbxError(err, stderr)
	}
	info.Version = parseSbxVersion(out)
	info.BinPath, info.ToolsPath = sandboxBinPath, sandboxToolsPath
	info.Sandboxes = []nodeproto.SandboxVM{}
	if _, avail, err := platform.Memory(); err == nil {
		info.FreeMemoryMB = int64(avail >> 20)
	}
	st, _ := a.loadSandboxState()
	info.Template = st.Template
	if !checkTemplate(info.Template) {
		info.Template = ""
	}

	out, stderr, err = d.run(ctx, nil, nil, "ls", "--json")
	list, parsed := parseSbxList(out)
	switch {
	case err == nil && parsed:
		info.Daemon, info.SignedIn = true, true
	case err == nil:
		info.Daemon, info.SignedIn = true, true
		info.Problem = "sbx ls --json printed what this node does not read"
	case sbxSaysSignIn(firstLine(stderr)):
		info.Daemon = true
		info.Problem = "sbx is not signed in to Docker: run sbx login on this machine"
	default:
		info.Problem = "sbx's daemon does not answer: " + orUnknownErr(firstLine(stderr), err)
	}
	for _, s := range list {
		brain, ok := strings.CutPrefix(s.Name, "fridolin-")
		if !ok || s.Name == "" {
			continue
		}
		info.Sandboxes = append(info.Sandboxes, nodeproto.SandboxVM{Name: s.Name, Brain: brain, Status: s.Status, MemoryMB: st.memory(brain)})
	}

	settings, _, serr := d.run(ctx, nil, nil, "settings", "ls")
	policy, _, perr := d.run(ctx, nil, nil, "policy", "ls")
	info.Settings = serr == nil && perr == nil && sbxSettingsHold(settings, policy)

	if a.claudeToken() != "" {
		info.Providers = append(info.Providers, nodeproto.SandboxProviderClaude)
	}
	// The listing is never logged: sbx prints parts of some values.
	if secrets, _, err := d.run(ctx, nil, nil, "secret", "ls", "--json"); err == nil && sbxCodexLogin(secrets) {
		info.Providers = append(info.Providers, nodeproto.SandboxProviderCodex)
	}

	if info.Problem == "" {
		switch {
		case !info.Settings:
			info.Problem = "sbx's global settings are not fridolin's (network policy deny-all, SSH agent forwarding and the browser off)"
		case info.Template == "":
			info.Problem = "the sandbox template is not set up yet"
		case len(info.Providers) == 0:
			info.Problem = "neither the Claude token nor sbx's Codex login is on this machine"
		}
	}
	return info, nil
}

func checkTemplate(t string) bool {
	c := nodeproto.SandboxCap{Template: t}
	return c.Check() == nil
}

func orUnknownErr(msg string, err error) string {
	if msg != "" {
		return msg
	}
	if err != nil {
		return err.Error()
	}
	return "no answer"
}

// claudeToken is the Claude token runs log in with, from the host's
// secret store, "" when there is none.
func (a *Agent) claudeToken() string {
	if a.CoreSecrets == nil {
		return ""
	}
	tok, err := a.CoreSecrets.Get(hostsecrets.ClaudeOAuthToken)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(tok)
}

// sandboxCheck asks sbx again, when it is due or force says so, and
// reports whether what the node declares changed.
func (a *Agent) sandboxCheck(ctx context.Context, force bool) bool {
	sb := &a.sandbox
	sb.mu.Lock()
	due := force || sb.checked.IsZero() || time.Since(sb.checked) >= sandboxCheckEvery
	sb.mu.Unlock()
	if !due {
		return false
	}
	info, err := a.sandboxProbe(ctx)
	var next *nodeproto.SandboxCap
	msg := ""
	if err == nil {
		c := info.SandboxCap
		if c.Check() != nil {
			c = nodeproto.SandboxCap{Version: "", Problem: "sbx answered with what this node does not read"}
		}
		next = &c
	} else if !errors.Is(err, errNoSbx) {
		msg = err.Error()
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.checked = time.Now()
	old := sb.cap
	sb.cap = next
	if msg != "" && msg != sb.lastErr {
		a.logf("node: sbx does not run, so no sandbox: %s", msg)
	}
	sb.lastErr = msg
	changed := (old == nil) != (next == nil) || old != nil && !sameSandboxCap(*old, *next)
	if changed && next != nil {
		a.logf("node: offering sandbox (sbx %s)%s", orUnknown(next.Version), problemNote(next.Problem))
	}
	return changed
}

func problemNote(p string) string {
	if p == "" {
		return ""
	}
	return "; not ready: " + p
}

func sameSandboxCap(a, b nodeproto.SandboxCap) bool {
	return a.Version == b.Version && a.SignedIn == b.SignedIn && a.Daemon == b.Daemon && a.Settings == b.Settings &&
		a.Template == b.Template && a.Problem == b.Problem && slices.Equal(a.Providers, b.Providers)
}

// sandboxDeclared is the sandbox the node declares now.
func (a *Agent) sandboxDeclared() *nodeproto.SandboxCap {
	a.sandbox.mu.Lock()
	defer a.sandbox.mu.Unlock()
	if a.sandbox.cap == nil {
		return nil
	}
	c := *a.sandbox.cap
	c.Providers = slices.Clone(c.Providers)
	return &c
}
