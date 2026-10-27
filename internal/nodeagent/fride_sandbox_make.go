package nodeagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// frideCloneTimeout bounds a starter repository's clone on the host.
const frideCloneTimeout = 10 * time.Minute

// FrideSandboxCreate makes the sandboxed FrIDE: the template when sbx
// lacks it, the sandbox with the base folder, its port and its egress,
// FrIDE's config and git identity, and FrIDE built at the pinned commit.
// FrIDE does not run yet: start does that, and pairs.
func FrideSandboxCreate(ctx context.Context, o FrideSandboxOptions) error {
	cfg, err := LoadConfig(o.Dir)
	if err != nil {
		return fmt.Errorf("%w (join first: fridolin node fride sandbox join)", err)
	}
	if err := checkFrideSandboxPair(cfg, o.frideURL()); err != nil {
		return err
	}
	if st, err := LoadFrideSandbox(o.Dir); err != nil {
		return err
	} else if st != nil {
		return fmt.Errorf("the sandboxed FrIDE is made already (base %s): fridolin node fride sandbox update, or rm first", st.Base)
	}
	d := o.sbx()
	if out, stderr, err := d.run(ctx, nil, nil, "version"); err != nil {
		return fmt.Errorf("sbx does not run: %w", sbxError(err, stderr))
	} else if v := parseSbxVersion(out); v != TestedSbxVersion {
		fmt.Fprintf(o.Out, "Note: sbx is %s; fridolin is tested with %s.\n", orUnknown(v), TestedSbxVersion)
	}
	diag, _, problem := sbxState(ctx, d)
	switch {
	case !diag.Daemon:
		return errors.New(problem)
	case !diag.SignedIn:
		return errSbxSignedOut
	}
	if status, err := o.sandboxStatus(ctx); err != nil {
		return err
	} else if status != "" {
		return fmt.Errorf("sbx has a sandbox %s already (%s): fridolin node fride sandbox rm", o.sandbox(), status)
	}
	commit, err := FridePin(o.Repo)
	if err != nil {
		return err
	}
	if o.FrideRepo == "" {
		if o.Git == "" {
			return errors.New("FrIDE's git URL is missing: pass --fride-repo")
		}
		origin, err := o.gitOut(ctx, "-C", o.Repo, "remote", "get-url", "origin")
		if err != nil {
			return fmt.Errorf("fridolin's origin in %s: %w (pass --fride-repo)", o.Repo, err)
		}
		if o.FrideRepo, err = frideRepoFrom(origin); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(o.Base, 0o755); err != nil {
		return err
	}
	base, err := checkBaseDir(o.Base)
	if err != nil {
		return fmt.Errorf("--base: %w", err)
	}
	config, err := companionConfig(o.Repo, base)
	if err != nil {
		return err
	}
	tag, err := FrideSandboxTemplateTag(o.Repo)
	if err != nil {
		return err
	}
	disk, err := checkDockerDisk(o.DockerDisk)
	if err != nil {
		return err
	}
	caches, err := prepareCaches(o.Caches, base)
	if err != nil {
		return fmt.Errorf("--caches: %w", err)
	}
	if err := o.ensureTemplate(ctx, tag); err != nil {
		return err
	}
	o.starters(ctx, base)

	st := FrideSandboxState{Name: o.sandbox(), Base: base, Template: tag, Repo: o.FrideRepo, DockerDisk: disk, Caches: caches}
	if err := o.newSandbox(ctx, st); err != nil {
		return err
	}
	if err := o.makeInside(ctx, &st, config, commit); err != nil {
		_, _, _ = d.run(context.WithoutCancel(ctx), nil, nil, "rm", "-f", o.sandbox())
		return fmt.Errorf("%w; the sandbox is removed again", err)
	}
	if err := saveFrideSandbox(o.Dir, st); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "Made the sandboxed FrIDE (%s). Next: its agents' Claude login (fridolin node fride sandbox login), then fridolin node fride sandbox start.\n", short(commit))
	return nil
}

// ensureTemplate builds the template tag and loads it into sbx, unless
// sbx has it.
func (o FrideSandboxOptions) ensureTemplate(ctx context.Context, tag string) error {
	d := o.sbx()
	if sbxHasTemplate(ctx, d, tag) {
		fmt.Fprintf(o.Out, "sbx has the template %s already.\n", tag)
		return nil
	}
	so := SandboxSetupOptions{Dir: o.Dir, Sbx: o.Sbx, Docker: o.Docker, Repo: o.Repo, Out: o.Out, env: o.env}
	return so.buildTemplate(ctx, d, tag, filepath.Join(o.Repo, filepath.FromSlash(frideSandboxDockerfile)))
}

// newSandbox makes st's sandbox with sbx create: its template, memory
// and port, the base folder and the caches read-write at their own
// paths, the caches' environment, and its Docker disk.
func (o FrideSandboxOptions) newSandbox(ctx context.Context, st FrideSandboxState) error {
	caches := "no shared caches"
	if st.Caches != "" {
		caches = "the caches in " + st.Caches
	}
	fmt.Fprintf(o.Out, "Creating the sandbox %s (memory %s, Docker disk %s, FrIDE on 127.0.0.1:%d, the repositories in %s, %s)…\n",
		st.Name, frideSandboxMemory, st.DockerDisk, FrideSandboxPort, st.Base, caches)
	args := []string{"create", "--name", st.Name, "--memory", frideSandboxMemory, "-t", st.Template, "--pull", "never",
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", FrideSandboxPort, FrideSandboxPort)}
	for _, kv := range frideSandboxCacheEnv(st.Caches) {
		args = append(args, "-e", kv)
	}
	args = append(args, "claude", st.Base)
	if st.Caches != "" {
		args = append(args, st.Caches)
	}
	var env []string
	if st.DockerDisk != "" {
		env = []string{frideSandboxDiskEnv + "=" + st.DockerDisk}
	}
	if _, stderr, err := o.sbx().run(ctx, nil, env, args...); err != nil {
		return fmt.Errorf("sbx could not create %s: %w", st.Name, sbxError(err, stderr))
	}
	return nil
}

// makeInside sets the new sandbox's rules, then prepares FrIDE's folders
// and config, the git identity, and builds FrIDE at commit.
func (o FrideSandboxOptions) makeInside(ctx context.Context, st *FrideSandboxState, config []byte, commit string) error {
	d := o.sbx()
	rules := [][]string{
		{"allow", "localhost:" + strconv.Itoa(FrideSandboxWebhookPort)},
		{"allow", strings.Join(frideSandboxEgress, ",")},
	}
	for _, h := range sandboxClaudeDenied {
		rules = append(rules, []string{"deny", h})
	}
	for _, r := range rules {
		if _, stderr, err := d.run(ctx, nil, nil, "policy", r[0], "network", "--sandbox", o.sandbox(), r[1]); err != nil {
			return fmt.Errorf("sbx could not %s %s: %w", r[0], r[1], sbxError(err, stderr))
		}
	}
	if err := o.exec(ctx, bytes.NewReader(config), frideSandboxPrepare); err != nil {
		return err
	}
	if err := o.gitIdentity(ctx); err != nil {
		return err
	}
	return o.build(ctx, st, commit)
}

// gitIdentity sets the git identity when one is given, and says when
// none is.
func (o FrideSandboxOptions) gitIdentity(ctx context.Context) error {
	if o.GitName == "" && o.GitEmail == "" {
		fmt.Fprintln(o.Out, "Note: no git identity given (--git-name, --git-email): an agent's first commit stops at a question until there is one.")
		return nil
	}
	return o.exec(ctx, nil, frideSandboxGit, o.GitName, o.GitEmail)
}

// build builds FrIDE at commit in the sandbox and records it in st.
func (o FrideSandboxOptions) build(ctx context.Context, st *FrideSandboxState, commit string) error {
	fmt.Fprintf(o.Out, "Building FrIDE %s in the sandbox (a minute or two the first time)…\n", short(commit))
	if err := o.exec(ctx, nil, frideSandboxBuild, st.Repo, commit); err != nil {
		return err
	}
	st.Commit, st.Built = commit, time.Now().UTC().Truncate(time.Second)
	return nil
}

// starters clones fridolin and FrIDE into base on the host, with the
// host's git and credentials, where they are not yet: FrIDE starts with
// them, as the native companion does. A clone that fails is a note.
func (o FrideSandboxOptions) starters(ctx context.Context, base string) {
	if o.Git == "" {
		return
	}
	origin, _ := o.gitOut(ctx, "-C", o.Repo, "remote", "get-url", "origin")
	for name, url := range map[string]string{"fridolin": origin, "fride": o.FrideRepo} {
		dst := filepath.Join(base, name)
		if _, err := os.Stat(dst); err == nil || url == "" {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, frideCloneTimeout)
		c := exec.CommandContext(cctx, o.Git, "clone", "-q", url, dst)
		c.Env = append(c.Environ(), "GIT_TERMINAL_PROMPT=0")
		c.Stdout, c.Stderr = o.Out, o.Out
		if err := c.Run(); err != nil {
			fmt.Fprintf(o.Out, "Note: cloning %s into %s failed (%v); FrIDE starts without it.\n", name, base, err)
			_ = os.RemoveAll(dst)
		} else {
			fmt.Fprintf(o.Out, "Cloned %s into %s.\n", name, base)
		}
		cancel()
	}
}

// short is a commit's first 12 characters.
func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
