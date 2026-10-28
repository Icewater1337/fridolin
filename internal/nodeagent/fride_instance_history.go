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

// An instance's history (docs/NODES.md, "History"): what lives only on
// the VM's disk and would go with the sandbox. Removing an instance saves
// it to the instance's folder in the node's state dir with sbx cp,
// merging into what an earlier removal saved, so that nothing an agent
// pruned since is lost; making the instance again puts it back. A remake
// (another memory or template) carries more: the workspaces, the
// repositories, the login and the settings too.

// frideInstanceHistory are the paths, under the VM's home, of the
// agents' sessions (Claude Code's and Codex's, which --resume lists) and
// FrIDE's state: its tasks, journal, pairing and config.
var frideInstanceHistory = []string{
	".claude/projects", ".claude/history.jsonl", ".claude/file-history",
	".codex/sessions", ".codex/history.jsonl",
	"fride-companion/home",
}

// frideInstanceCarry is what a remake carries into the new sandbox: the
// history, the workspaces, the repositories, the Claude login and Claude
// Code's and git's settings. What is not history is kept only until it is
// back.
var frideInstanceCarry = append(slices.Clone(frideInstanceHistory),
	"fride-companion/workspaces", "fride-companion/repos", ".claude/settings.json", ".claude.json", ".gitconfig")

// historyDir is the folder of an instance's saved history.
func (o instanceOps) historyDir(name string) string {
	return filepath.Join(frideInstanceDir(o.dir, name), frideInstanceHistoryDir)
}

// carryDir is the folder of what a remake carries. It stays while that
// is not done, so that the next start can finish it.
func (o instanceOps) carryDir(name string) string {
	return filepath.Join(frideInstanceDir(o.dir, name), frideInstanceCarryDir)
}

// carrying reports whether a remake left a carry to finish with.
func (o instanceOps) carrying(name string) bool {
	_, err := os.Stat(o.carryDir(name))
	return err == nil
}

// vmHas is the VM's home and which of paths are there.
func (o instanceOps) vmHas(ctx context.Context, sandbox string, paths []string) (string, []string, error) {
	args := append([]string{"exec", sandbox}, frideInstanceScript(frideInstanceList, paths...)...)
	out, stderr, err := o.d.run(ctx, nil, nil, args...)
	if err != nil {
		return "", nil, fmt.Errorf("in the sandbox: %w", sbxError(err, lastLineOf(stderr)))
	}
	kv := parseKeyValues(out)
	home := kv["home"]
	if !path.IsAbs(home) {
		return "", nil, errors.New("the sandbox does not say where its home is")
	}
	// What the VM says only picks from paths: a path the node copies to
	// or from is always one of its own, never one the VM made up.
	said := map[string]bool{}
	for line := range strings.Lines(string(out)) {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "have="); ok {
			said[p] = true
		}
	}
	var have []string
	for _, p := range paths {
		if said[p] {
			have = append(have, p)
		}
	}
	return home, have, nil
}

// saveVM copies those of paths the VM has into store, merging into what
// is there, and returns how many it copied.
func (o instanceOps) saveVM(ctx context.Context, sandbox, store string, paths []string) (int, error) {
	home, have, err := o.vmHas(ctx, sandbox, paths)
	if err != nil {
		return 0, err
	}
	if len(have) > 0 {
		// sbx cp writes there as its user. A store made for nothing would
		// stand for a remake's carry.
		if err := os.MkdirAll(store, 0o700); err != nil {
			return 0, err
		}
		if err := o.d.hostFolder(store); err != nil {
			return 0, err
		}
	}
	for _, p := range have {
		dst := filepath.Join(store, filepath.FromSlash(path.Dir(p)))
		if err := os.MkdirAll(dst, 0o700); err != nil {
			return 0, err
		}
		if _, stderr, err := o.d.run(ctx, nil, nil, "cp", sandbox+":"+path.Join(home, p), dst+string(filepath.Separator)); err != nil {
			return 0, fmt.Errorf("sbx could not copy %s out of the sandbox: %w", p, sbxError(err, stderr))
		}
	}
	return len(have), nil
}

// restoreVM copies those of paths store has into the VM, into what is
// there, and gives them to the VM's user (sbx cp gives them the host's
// uid); FrIDE's pid file of the old VM goes. It returns how many it
// copied.
func (o instanceOps) restoreVM(ctx context.Context, sandbox, store string, paths []string) (int, error) {
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
	// sbx cp reads there as its user.
	if err := o.d.hostFolder(store); err != nil {
		return 0, err
	}
	home, _, err := o.vmHas(ctx, sandbox, nil)
	if err != nil {
		return 0, err
	}
	parents := make([]string, 0, len(have))
	for _, p := range have {
		parents = append(parents, path.Dir(p))
	}
	if err := o.exec(ctx, sandbox, nil, frideInstanceMkdirs, parents...); err != nil {
		return 0, err
	}
	for _, p := range have {
		src := filepath.Join(store, filepath.FromSlash(p))
		if _, stderr, err := o.d.run(ctx, nil, nil, "cp", src, sandbox+":"+path.Join(home, path.Dir(p))+"/"); err != nil {
			return 0, fmt.Errorf("sbx could not copy %s into the sandbox: %w", p, sbxError(err, stderr))
		}
	}
	args := append([]string{"exec", "-u", "root", sandbox}, frideInstanceScript(frideInstanceOwn, append([]string{home}, have...)...)...)
	if _, stderr, err := o.d.run(ctx, nil, nil, args...); err != nil {
		return 0, fmt.Errorf("in the sandbox: %w", sbxError(err, lastLineOf(stderr)))
	}
	return len(have), nil
}

// saveHistory ends FrIDE, so that its state rests, and saves the
// instance's history into its history folder.
func (o instanceOps) saveHistory(ctx context.Context, st *FrideInstanceState, status string) error {
	if status == "running" {
		if err := o.exec(ctx, st.Sandbox, nil, frideInstanceStop); err != nil {
			return err
		}
	}
	n, err := o.saveVM(ctx, st.Sandbox, o.historyDir(st.Name), frideInstanceHistory)
	if err != nil {
		return fmt.Errorf("saving the history: %w", err)
	}
	o.say("Saved %d folders and files of %s's history (the agents' sessions, FrIDE's tasks) to %s.", n, st.Name, o.historyDir(st.Name))
	return nil
}
