package nodeagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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

// frideInstanceTarball is the file in the carry folder that holds the
// carry's repositories, workspaces and settings, made by tar in the VM:
// sbx cp would extract them on the host, where a symlink in a repository
// needs a privilege a Windows node's user lacks. The history stays
// plain files, which a later start merges (keepCarryHistory); an older
// carry holds everything as plain files, and restoreVM still reads it.
const frideInstanceTarball = "carry.tar.gz"

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

// keepCarryHistory moves the history in the carry an unfinished remake
// left into the history folder, merging into what an earlier removal
// saved, as saving it from the VM would have. On an error what is not
// moved stays in the carry.
func (o instanceOps) keepCarryHistory(name string) error {
	carry, store := o.carryDir(name), o.historyDir(name)
	n := 0
	for _, p := range frideInstanceHistory {
		src := filepath.Join(carry, filepath.FromSlash(p))
		if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := mergeMove(src, filepath.Join(store, filepath.FromSlash(p))); err != nil {
			return fmt.Errorf("keeping the history of an unfinished remake: %w", err)
		}
		n++
	}
	o.say("Kept %d folders and files of %s's history from the unfinished remake's %s in %s.", n, name, carry, store)
	return nil
}

// mergeMove moves src to dst; into a folder dst a folder src merges, a
// file of src replacing the one of dst. Both are on the node's disk, in
// the instance's folder.
func mergeMove(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	sfi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	dfi, err := os.Lstat(dst)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !sfi.IsDir() && !dfi.IsDir()) {
		return os.Rename(src, dst)
	}
	if err != nil {
		return err
	}
	if !sfi.IsDir() || !dfi.IsDir() {
		return fmt.Errorf("%s and %s are not both folders", src, dst)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := mergeMove(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
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
	var tarred []string
	for _, p := range have {
		if !slices.Contains(frideInstanceHistory, p) {
			tarred = append(tarred, p)
			continue
		}
		dst := filepath.Join(store, filepath.FromSlash(path.Dir(p)))
		if err := os.MkdirAll(dst, 0o700); err != nil {
			return 0, err
		}
		if _, stderr, err := o.d.run(ctx, nil, nil, "cp", sandbox+":"+path.Join(home, p), dst+string(filepath.Separator)); err != nil {
			return 0, fmt.Errorf("sbx could not copy %s out of the sandbox: %w", p, sbxError(err, stderr))
		}
	}
	if len(tarred) > 0 {
		if err := o.tarOut(sandbox, filepath.Join(store, frideInstanceTarball), home, tarred); err != nil {
			return 0, err
		}
	}
	return len(have), nil
}

// tarOut has the VM tar those of paths under home into the file tarball
// on the host, which only stores it. It tars as root, which reads what
// another uid owns (a database container's data folder), and takes tar's
// exit status 1, files that changed while it read, as done. The tarball
// is written aside and only then replaces an earlier one, so a failed
// tar keeps what was saved.
func (o instanceOps) tarOut(sandbox, tarball, home string, paths []string) error {
	part := tarball + ".part"
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(part)
	defer f.Close()
	var stderr bytes.Buffer
	c, err := o.d.start(nil, f, &stderr, nil, append([]string{"exec", "-u", "root", sandbox, "tar", "--warning=no-file-changed", "-cz", "-C", home}, paths...)...)
	if err == nil {
		err = c.Wait()
	}
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) && ee.ExitCode() == 1 {
		err = nil
	}
	if err != nil {
		return fmt.Errorf("sbx could not tar %s out of the sandbox: %w", strings.Join(paths, ", "), sbxError(err, stderr.Bytes()))
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(part, tarball)
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
	tarball := filepath.Join(store, frideInstanceTarball)
	_, terr := os.Stat(tarball)
	if terr != nil && !errors.Is(terr, fs.ErrNotExist) {
		return 0, terr
	}
	if len(have) == 0 && terr != nil {
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
	if terr == nil {
		f, err := os.Open(tarball)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		if _, stderr, err := o.d.run(ctx, f, nil, "exec", "-i", sandbox, "tar", "-xz", "-C", home); err != nil {
			return 0, fmt.Errorf("sbx could not untar the saved things into the sandbox: %w", sbxError(err, stderr))
		}
		have = append(have, frideInstanceCarry...)
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
