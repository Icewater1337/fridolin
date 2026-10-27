package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// A brain's sessions survive a rebuild of its sandbox (#149). sbx's
// Claude kit keeps ~/.claude/projects on a volume of the sandbox's own,
// which `sbx rm` deletes, and ensure makes a sandbox again when its
// template, mounts, memory or egress change. So the node keeps the
// sessions on the host, in <state dir>/sandbox/history/<brain>/<provider>,
// and binds that folder over the VM's with `sbx mount NAME HOST:TARGET`,
// which sbx restores at each start. Nothing is copied: the sessions are
// on the host as they are written. Removing the sandbox keeps them.

const (
	// sandboxHistoryDirName is the folder of the brains' sessions, in
	// the state dir's sandbox folder.
	sandboxHistoryDirName = "history"
	// sandboxHome is the home folder of the kit's user in the VM.
	sandboxHome = "/home/agent" // personal-data:ignore (the kit's user in the VM)
)

// sandboxHistoryTarget is where provider keeps its sessions in the VM.
// Codex runs pass --ephemeral and write none today.
func sandboxHistoryTarget(provider string) string {
	if provider == nodeproto.SandboxProviderCodex {
		return sandboxHome + "/.codex/sessions"
	}
	return sandboxHome + "/.claude/projects"
}

// sandboxBrainIDRe is a brain's id: one name, never a path.
var sandboxBrainIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// sandboxHistoryDir is the host folder of brain's sessions with
// provider. brain must be an id: one name in the history folder.
func (a *Agent) sandboxHistoryDir(brain, provider string) (string, error) {
	if a.Dir == "" {
		return "", errors.New("this agent has no state dir")
	}
	if !sandboxBrainIDRe.MatchString(brain) {
		return "", fmt.Errorf("%q is not a brain id", brain)
	}
	name := filepath.Base(brain)
	if name != brain || strings.Contains(name, "..") {
		return "", fmt.Errorf("%q is not a brain id", brain)
	}
	sub := nodeproto.SandboxProviderClaude
	if provider == nodeproto.SandboxProviderCodex {
		sub = nodeproto.SandboxProviderCodex
	}
	return filepath.Join(a.Dir, sandboxDir, sandboxHistoryDirName, name, sub), nil
}

// sandboxHistoryFolder makes the host folder of the spec's sessions, so
// that sbx finds it when it restores the mount at a start.
func (a *Agent) sandboxHistoryFolder(brain string, s sandboxSpec) (string, *nodeproto.Error) {
	dir, err := a.sandboxHistoryDir(brain, s.Provider)
	if err != nil {
		return "", nodeproto.Errorf(nodeproto.CodeFailed, "the folder of the sessions: %v", err)
	}
	if strings.ContainsAny(dir, ":,") {
		return "", nodeproto.Errorf(nodeproto.CodeFailed, "the state dir %s has a : or , that sbx's mounts split on", a.Dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nodeproto.Errorf(nodeproto.CodeFailed, "the folder of %s's sessions: %v", brain, err)
	}
	return dir, nil
}

// sandboxMountHistory binds the host folder of the brain's sessions over
// the VM's, after create. sbx mounts into a running sandbox only:
// ensure's exec has just started it, and when it stopped since, an exec
// starts it again. A mount that fails fails ensure (CodeFailed).
func (a *Agent) sandboxMountHistory(ctx context.Context, d sbxDriver, name, brain string, s sandboxSpec) *nodeproto.Error {
	if s.History == "" {
		return nil
	}
	dir, perr := a.sandboxHistoryFolder(brain, s)
	if perr != nil {
		return perr
	}
	spec := dir + ":" + s.History
	_, stderr, err := d.run(ctx, nil, nil, "mount", name, spec)
	if err != nil {
		_, _, _ = d.run(ctx, nil, nil, "exec", name, "true")
		_, stderr, err = d.run(ctx, nil, nil, "mount", name, spec)
	}
	if err != nil {
		return nodeproto.Errorf(nodeproto.CodeFailed, "keeping %s's sessions on the host: %v", name, sbxError(err, stderr))
	}
	return nil
}
