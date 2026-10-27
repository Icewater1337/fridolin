package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// The hosts a Claude sandbox may not reach although the kit opens them:
// updates (the template pins Claude Code) and remote control.
var sandboxClaudeDenied = []string{"downloads.claude.ai", "bridge.claudeusercontent.com"}

// checkMarker reports whether folder holds the core's runtime marker:
// that the core's brain folder and this node's are the same.
func checkMarker(folder, marker string) *nodeproto.Error {
	if err := nodeproto.CheckAbsPath(folder); err != nil {
		return nodeproto.Errorf(nodeproto.CodeBadRequest, "the brain's folder: %v", err)
	}
	// The marker is one name in the folder, and no link out of it.
	f, err := os.OpenInRoot(folder, nodeproto.SandboxMarkerFile)
	if err != nil {
		return nodeproto.Errorf(nodeproto.CodeUnavailable, "the brain's folder here holds no runtime marker: it is not the core's folder")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, nodeproto.MaxSandboxMarkerLen+2))
	if err != nil || strings.TrimSpace(string(b)) != marker {
		return nodeproto.Errorf(nodeproto.CodeUnavailable, "the brain's folder here holds another runtime marker: it is not the core's folder")
	}
	return nil
}

// sandboxSpec is what a brain's sandbox is made with; its key decides
// whether ensure must make it again.
type sandboxSpec struct {
	Folder   string                   `json:"folder"`
	Mounts   []nodeproto.SandboxMount `json:"mounts"`
	MemoryMB int                      `json:"memory_mb"`
	Template string                   `json:"template"`
	Egress   []string                 `json:"egress"`
	APIPort  int                      `json:"api_port"`
	Provider string                   `json:"provider"`
}

func (s sandboxSpec) key() string {
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// createArgs is `sbx create` for the spec: the brain's folder read-write
// first, then the other mounts, each at its host path.
func (s sandboxSpec) createArgs(name string) []string {
	args := []string{"create", "--name", name, "--memory", fmt.Sprintf("%dm", s.MemoryMB)}
	if s.Template != "" {
		args = append(args, "-t", s.Template, "--pull", "never")
	}
	args = append(args, s.Provider, s.Folder)
	for _, m := range s.Mounts {
		if m.ReadOnly {
			args = append(args, m.Path+":ro")
		} else {
			args = append(args, m.Path)
		}
	}
	return args
}

// specOf is the spec ensure asks for, with the node's template and the
// defaults filled in. A mount of the folder itself is left out.
func (a *Agent) specOf(p nodeproto.SandboxEnsureParams, template string) sandboxSpec {
	s := sandboxSpec{Folder: p.Folder, MemoryMB: p.MemoryMB, Template: template, Egress: slices.Clone(p.Egress), APIPort: p.APIPort, Provider: p.Provider}
	if s.MemoryMB == 0 {
		s.MemoryMB = nodeproto.DefaultSandboxMemoryMB
	}
	for _, m := range p.Mounts {
		if m.Path != p.Folder && !slices.ContainsFunc(s.Mounts, func(o nodeproto.SandboxMount) bool { return o.Path == m.Path }) {
			s.Mounts = append(s.Mounts, m)
		}
	}
	// The linux fridolin's folder, read-only: sbx mounts a folder only at
	// its host path, so ensure links the binary to sandboxBinPath.
	if a.Dir != "" {
		s.Mounts = append(s.Mounts, nodeproto.SandboxMount{Path: a.sandboxBinHostDir(), ReadOnly: true})
	}
	if s.Mounts == nil {
		s.Mounts = []nodeproto.SandboxMount{}
	}
	if s.Egress == nil {
		s.Egress = []string{}
	}
	return s
}

// sandboxEnsure is sandbox.ensure: the brain's sandbox exists as p asks,
// made again when what it was made with changed.
func (a *Agent) sandboxEnsure(ctx context.Context, p nodeproto.SandboxEnsureParams) (any, *nodeproto.Error) {
	if err := p.Check(); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	if perr := checkMarker(p.Folder, p.Marker); perr != nil {
		return nil, perr
	}
	c, perr := a.sandboxReady(ctx, p.Provider)
	if perr != nil {
		return nil, perr
	}
	d := a.sbx()
	if d == nil {
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "sbx is not installed on this node")
	}
	name := nodeproto.SandboxName(p.Brain)
	spec := a.specOf(p, c.Template)
	key := spec.key()

	sb := &a.sandbox
	sb.ensure.Lock()
	defer sb.ensure.Unlock()
	st, err := a.loadSandboxState()
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	out, stderr, err := d.run(ctx, nil, nil, "ls", "--json")
	list, ok := parseSbxList(out)
	if err != nil || !ok {
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "sbx does not list its sandboxes: %s", orUnknownErr(firstLine(stderr), err))
	}
	exists := slices.ContainsFunc(list, func(s sbxSandbox) bool { return s.Name == name })
	state := nodeproto.SandboxCreated
	if exists {
		if st.Sandboxes[p.Brain].Key == key {
			return nodeproto.SandboxEnsureResult{Name: name, State: nodeproto.SandboxReady}, nil
		}
		if a.sandboxBusy(p.Brain) {
			return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "a run is going on in %s, which must be made again", name)
		}
		if _, stderr, err := d.run(ctx, nil, nil, "rm", "-f", name); err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "removing %s to make it again: %v", name, sbxError(err, stderr))
		}
		state = nodeproto.SandboxRecreated
	}
	delete(st.Sandboxes, p.Brain)
	if err := a.saveSandboxState(st); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	if _, stderr, err := d.run(ctx, nil, nil, spec.createArgs(name)...); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "sbx could not create %s: %v", name, sbxError(err, stderr))
	}
	perr = a.sandboxRules(ctx, d, name, spec)
	if perr == nil {
		perr = a.sandboxLinkBin(ctx, d, name)
	}
	if perr != nil {
		_, _, _ = d.run(context.WithoutCancel(ctx), nil, nil, "rm", "-f", name)
		return nil, perr
	}
	if st.Sandboxes == nil {
		st.Sandboxes = map[string]sandboxRecord{}
	}
	st.Sandboxes[p.Brain] = sandboxRecord{Key: key, MemoryMB: spec.MemoryMB}
	if err := a.saveSandboxState(st); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	a.logf("node: sandbox %s %s", name, state)
	return nodeproto.SandboxEnsureResult{Name: name, State: state}, nil
}

// sandboxRules sets the sandbox's own network rules: the core's API on
// the host, the brain's egress, and for Claude the kit's hosts it may
// not reach. A rule that cannot be set fails ensure (CodeFailed: the
// core refuses the run rather than run it with other rules).
func (a *Agent) sandboxRules(ctx context.Context, d sbxDriver, name string, s sandboxSpec) *nodeproto.Error {
	rules := [][]string{{"allow", fmt.Sprintf("localhost:%d", s.APIPort)}}
	if len(s.Egress) > 0 {
		rules = append(rules, []string{"allow", strings.Join(s.Egress, ",")})
	}
	if s.Provider == nodeproto.SandboxProviderClaude {
		for _, h := range sandboxClaudeDenied {
			rules = append(rules, []string{"deny", h})
		}
	}
	for _, r := range rules {
		if _, stderr, err := d.run(ctx, nil, nil, "policy", r[0], "network", "--sandbox", name, r[1]); err != nil {
			return nodeproto.Errorf(nodeproto.CodeFailed, "sbx could not %s %s for %s: %v", r[0], r[1], name, sbxError(err, stderr))
		}
	}
	return nil
}

// sandboxLinkBin links the linux fridolin on its read-only mount to
// sandboxBinPath in the VM, whose disk keeps the link across stops. A
// link that cannot be made fails ensure (CodeFailed).
func (a *Agent) sandboxLinkBin(ctx context.Context, d sbxDriver, name string) *nodeproto.Error {
	if a.Dir == "" {
		return nil
	}
	bin := filepath.Join(a.sandboxBinHostDir(), sandboxBinFile)
	if _, stderr, err := d.run(ctx, nil, nil, "exec", "-u", "root", name, "sh", "-c", `mkdir -p "$(dirname "$1")" && ln -sfn "$0" "$1"`,
		bin, sandboxBinPath); err != nil {
		return nodeproto.Errorf(nodeproto.CodeFailed, "linking fridolin into %s: %v", name, sbxError(err, stderr))
	}
	return nil
}

// sandboxRemove is sandbox.remove.
func (a *Agent) sandboxRemove(ctx context.Context, p nodeproto.SandboxRemoveParams) (any, *nodeproto.Error) {
	if err := p.Check(); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	d := a.sbx()
	if d == nil {
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "sbx is not installed on this node")
	}
	name := nodeproto.SandboxName(p.Brain)
	sb := &a.sandbox
	sb.ensure.Lock()
	defer sb.ensure.Unlock()
	if a.sandboxBusy(p.Brain) {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "a run is going on in %s; stop it first", name)
	}
	out, stderr, err := d.run(ctx, nil, nil, "ls", "--json")
	list, ok := parseSbxList(out)
	if err != nil || !ok {
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "sbx does not list its sandboxes: %s", orUnknownErr(firstLine(stderr), err))
	}
	if slices.ContainsFunc(list, func(s sbxSandbox) bool { return s.Name == name }) {
		if _, stderr, err := d.run(ctx, nil, nil, "rm", "-f", name); err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeFailed, "sbx could not remove %s: %v", name, sbxError(err, stderr))
		}
	}
	st, err := a.loadSandboxState()
	if err == nil && st.Sandboxes != nil {
		delete(st.Sandboxes, p.Brain)
		err = a.saveSandboxState(st)
	}
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	a.logf("node: sandbox %s removed", name)
	return struct{}{}, nil
}

// sandboxReady is nil when brains may run here with provider now, else
// why not (CodeUnavailable: the core falls back). It asks sbx again
// when the last answer was not ready.
func (a *Agent) sandboxReady(ctx context.Context, provider string) (*nodeproto.SandboxCap, *nodeproto.Error) {
	c := a.sandboxDeclared()
	if c == nil || !c.Ready(provider) {
		if a.sandboxCheck(ctx, true) {
			a.declChanged()
		}
		c = a.sandboxDeclared()
	}
	switch {
	case c == nil:
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "sbx does not run on this node")
	case c.Problem != "":
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "%s", c.Problem)
	case !c.Ready(provider):
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "the %s login is not on this node", provider)
	}
	return c, nil
}
