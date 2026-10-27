package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// The sandboxed FrIDE's history (docs/NODES.md, "History"): what lives
// only on the VM's disk and would go with the sandbox. rm saves it to the
// node's state dir with sbx cp, merging into what an earlier rm saved,
// so that nothing an agent pruned since is lost; create puts it back.

// frideSandboxHistory are the paths, under the VM's home, of the agents'
// sessions (Claude Code's and Codex's, which --resume lists) and FrIDE's
// state: its tasks, journal, pairing and config.
var frideSandboxHistory = []string{
	".claude/projects", ".claude/history.jsonl", ".claude/file-history",
	".codex/sessions", ".codex/history.jsonl",
	"fride-companion/home",
}

// frideSandboxCarry is what update carries into a sandbox it makes anew
// for a new template: the history, the workspaces, the Claude login and
// Claude Code's and git's settings. What is not history is kept only
// until it is back.
var frideSandboxCarry = append(slices.Clone(frideSandboxHistory),
	"fride-companion/workspaces", ".claude/settings.json", ".claude.json", ".gitconfig")

// The folders, in the node's state dir, of the saved history and of
// what update carries.
const (
	frideSandboxHistoryDir = "fride-sandbox/history"
	frideSandboxCarryDir   = "fride-sandbox/carry"
)

// historyDir is the folder of the saved history.
func (o FrideSandboxOptions) historyDir() string {
	return filepath.Join(o.Dir, filepath.FromSlash(frideSandboxHistoryDir))
}

// vmHas is the VM's home and which of paths are there.
func (o FrideSandboxOptions) vmHas(ctx context.Context, paths []string) (string, []string, error) {
	args := append([]string{"exec", o.sandbox()}, frideSandboxScript(frideSandboxList, paths...)...)
	out, stderr, err := o.sbx().run(ctx, nil, nil, args...)
	if err != nil {
		return "", nil, fmt.Errorf("in the sandbox: %w", sbxError(err, lastLineOf(stderr)))
	}
	kv := parseKeyValues(out)
	home := kv["home"]
	if !path.IsAbs(home) {
		return "", nil, errors.New("the sandbox does not say where its home is")
	}
	var have []string
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "have="); ok && slices.Contains(paths, p) {
			have = append(have, p)
		}
	}
	return home, have, nil
}

// saveVM copies those of paths the VM has into store, merging into what
// is there, and returns how many it copied.
func (o FrideSandboxOptions) saveVM(ctx context.Context, store string, paths []string) (int, error) {
	home, have, err := o.vmHas(ctx, paths)
	if err != nil {
		return 0, err
	}
	for _, p := range have {
		dst := filepath.Join(store, filepath.FromSlash(path.Dir(p)))
		if err := os.MkdirAll(dst, 0o700); err != nil {
			return 0, err
		}
		if _, stderr, err := o.sbx().run(ctx, nil, nil, "cp", o.sandbox()+":"+path.Join(home, p), dst+string(filepath.Separator)); err != nil {
			return 0, fmt.Errorf("sbx could not copy %s out of the sandbox: %w", p, sbxError(err, stderr))
		}
	}
	return len(have), nil
}

// restoreVM copies those of paths store has into the VM, into what is
// there, and gives them to the VM's user (sbx cp gives them the host's
// uid); FrIDE's pid file of the old VM goes. It returns how many it
// copied.
func (o FrideSandboxOptions) restoreVM(ctx context.Context, store string, paths []string) (int, error) {
	var have []string
	for _, p := range paths {
		if _, err := os.Lstat(filepath.Join(store, filepath.FromSlash(p))); err == nil {
			have = append(have, p)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return 0, err
		}
	}
	if len(have) == 0 {
		return 0, nil
	}
	home, _, err := o.vmHas(ctx, nil)
	if err != nil {
		return 0, err
	}
	parents := make([]string, 0, len(have))
	for _, p := range have {
		parents = append(parents, path.Dir(p))
	}
	if err := o.exec(ctx, nil, frideSandboxMkdirs, parents...); err != nil {
		return 0, err
	}
	for _, p := range have {
		src := filepath.Join(store, filepath.FromSlash(p))
		if _, stderr, err := o.sbx().run(ctx, nil, nil, "cp", src, o.sandbox()+":"+path.Join(home, path.Dir(p))+"/"); err != nil {
			return 0, fmt.Errorf("sbx could not copy %s into the sandbox: %w", p, sbxError(err, stderr))
		}
	}
	args := append([]string{"exec", "-u", "root", o.sandbox()}, frideSandboxScript(frideSandboxOwn, append([]string{home}, have...)...)...)
	if _, stderr, err := o.sbx().run(ctx, nil, nil, args...); err != nil {
		return 0, fmt.Errorf("in the sandbox: %w", sbxError(err, lastLineOf(stderr)))
	}
	return len(have), nil
}
