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

// ErrSandboxNotSetUp is sbx on a node where `fridolin node sandbox
// setup` has not run: the node does not declare sandbox, so the owner is
// asked to approve it only once it can work.
var ErrSandboxNotSetUp = errors.New("not set up: run fridolin node sandbox setup")

// sandboxProbe asks sbx how it is: what sandbox.info answers. An error
// means sbx does not run at all (not found, or `sbx version` fails), or
// is not set up for fridolin (ErrSandboxNotSetUp).
func (a *Agent) sandboxProbe(ctx context.Context) (nodeproto.SandboxInfo, error) {
	var info nodeproto.SandboxInfo
	d := a.sbx()
	if d == nil || !a.sandboxOffered() {
		return info, errNoSbx
	}
	st, err := a.loadSandboxState()
	if err != nil {
		return info, err
	}
	if st.Template == "" || !checkTemplate(st.Template) {
		return info, ErrSandboxNotSetUp
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
	info.Template = st.Template
	info.Bin = a.sandboxBinVersion()

	diag, list, problem := sbxState(ctx, d)
	info.Daemon, info.SignedIn, info.Problem = diag.Daemon, diag.SignedIn, problem
	for _, s := range list {
		brain, ok := strings.CutPrefix(s.Name, "fridolin-")
		if !ok || s.Name == "" {
			continue
		}
		info.Sandboxes = append(info.Sandboxes, nodeproto.SandboxVM{Name: s.Name, Brain: brain, Status: s.Status, MemoryMB: st.memory(brain)})
	}

	settingsWhy := "sbx's daemon does not answer"
	tplThere := false
	if info.Daemon {
		info.Settings, settingsWhy = a.sandboxSettings(ctx, d)
		tplThere = sbxHasTemplate(ctx, d, info.Template)
	}
	info.Providers = a.sandboxProviders(ctx, d)

	if info.Problem == "" {
		switch {
		case !info.Settings:
			info.Problem = "sbx's global settings are not fridolin's (network policy deny-all, SSH agent forwarding and the browser off): " + settingsWhy
		case !tplThere:
			info.Problem = "sbx no longer has the template " + info.Template + ": run fridolin node sandbox setup again"
		case info.Bin == "":
			info.Problem = "the linux fridolin for the sandboxes is not here yet: the core sends it once the capability is approved"
		case len(info.Providers) == 0:
			info.Problem = "neither the Claude token nor sbx's Codex login is on this machine"
		}
	}
	if len(info.Problem) > nodeproto.MaxSandboxProblemLen {
		info.Problem = info.Problem[:nodeproto.MaxSandboxProblemLen-3] + "…"
	}
	return info, nil
}

// sbxHasTemplate reports whether sbx's image store holds the template.
func sbxHasTemplate(ctx context.Context, d sbxDriver, tpl string) bool {
	out, _, err := d.run(ctx, nil, nil, "template", "ls", "--json")
	return err == nil && sbxListsTemplate(out, tpl)
}

// sandboxProviders are the providers whose login is on this machine.
func (a *Agent) sandboxProviders(ctx context.Context, d sbxDriver) []string {
	var out []string
	if a.claudeToken() != "" {
		out = append(out, nodeproto.SandboxProviderClaude)
	}
	// The listing is never logged: sbx prints part of each value.
	if secrets, _, err := d.run(ctx, nil, nil, "secret", "ls", "-g", "--service", "openai", "--json"); err == nil && sbxCodexLogin(secrets) {
		out = append(out, nodeproto.SandboxProviderCodex)
	}
	return out
}

// sbxState is the daemon and Docker's sign-in, as `sbx diagnose --json`
// says, and fridolin's sandboxes from `sbx ls --json`. problem says why
// brains cannot run, "" when they can.
func sbxState(ctx context.Context, d sbxDriver) (sbxDiagnosis, []sbxSandbox, string) {
	// diagnose exits non-zero when a check fails, and still prints them.
	dout, _, _ := d.run(ctx, nil, nil, "diagnose", "--json")
	diag, known := parseSbxDiagnose(dout)
	out, stderr, err := d.run(ctx, nil, nil, "ls", "--json")
	list, parsed := parseSbxList(out)
	if !known {
		// An sbx without diagnose: what ls says.
		diag = sbxDiagnosis{Daemon: err == nil || sbxSaysSignIn(firstLine(stderr)), SignedIn: err == nil}
	}
	switch {
	case !diag.Daemon:
		return diag, list, "sbx's daemon does not answer: " + orUnknownErr(firstLine(stderr), err)
	case !diag.SignedIn:
		return diag, list, "sbx is not signed in to Docker: run sbx login on this machine"
	case err != nil:
		return diag, list, "sbx ls: " + orUnknownErr(firstLine(stderr), err)
	case !parsed:
		return diag, list, "sbx ls --json printed what this node does not read"
	}
	return diag, list, ""
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
		if errors.Is(err, ErrSandboxNotSetUp) {
			a.logf("node: sbx is here, but no sandbox is offered: %s", msg)
		} else {
			a.logf("node: sbx does not run, so no sandbox: %s", msg)
		}
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
		a.Template == b.Template && a.Bin == b.Bin && a.Problem == b.Problem && slices.Equal(a.Providers, b.Providers)
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
