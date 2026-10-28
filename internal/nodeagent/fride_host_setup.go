package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// FrideHostSetupOptions are what `fridolin node fride host setup` works
// with.
type FrideHostSetupOptions struct {
	// Dir is the node's state dir; the node must have joined.
	Dir string
	// Sbx, Docker and Git are the programs; Git "" needs FrideRepo.
	Sbx, Docker, Git string
	// Repo is a fridolin checkout: the template's Dockerfile, FrIDE's
	// pinned commit and the companion config.
	Repo string
	// FrideRepo is FrIDE's git URL ("": Repo's origin with fridolin
	// replaced by fride, on GitHub).
	FrideRepo string
	// DockerDisk is the size of a new sandbox's /var/lib/docker (""
	// DefaultFrideInstanceDockerDisk); Caches the host folder of the
	// instances' Go and npm caches, a folder of its own for each ("":
	// none).
	DockerDisk, Caches string
	// ReserveMB, when not 0, is the memory the node keeps for itself
	// (node.json's fride_host.reserve_mb).
	ReserveMB int
	// NoBuild builds no template: each start names the core's published
	// one, which sbx pulls (Docker, Git, Repo and FrideRepo are unused).
	NoBuild bool
	// Out gets what is done, for the person who runs it.
	Out io.Writer
	// env is added to each program's environment (tests: the fakes').
	env []string
}

// FrideHostSetup sets this machine up to run FrIDE instances: it checks
// sbx (running, signed in), makes sbx's global settings fridolin's,
// builds the template with FrIDE at the commit the checkout pins unless
// sbx has it (with NoBuild, it records that the template comes from the
// core instead), and records it all in fride-host.json, after which the
// node's agent offers fride_host. Run again, it builds the template of a
// newer checkout; instances get it when their sandbox is made anew.
func FrideHostSetup(ctx context.Context, o FrideHostSetupOptions) error {
	cfg, err := LoadConfig(o.Dir)
	if err != nil {
		return fmt.Errorf("%w (join first: fridolin node join)", err)
	}
	if cfg.Box != nil {
		return errors.New("a node in a box runs no FrIDE instances: set them up on a native node")
	}
	d := sbxCLI{path: o.Sbx, env: o.env}
	if out, stderr, err := d.run(ctx, nil, nil, "version"); err != nil {
		return fmt.Errorf("sbx does not run: %w", sbxError(err, stderr))
	} else if v := parseSbxVersion(out); v != TestedSbxVersion {
		fmt.Fprintf(o.Out, "Note: sbx is %s; fridolin is tested with %s.\n", orUnknown(v), TestedSbxVersion)
	}
	diag, list, problem := sbxState(ctx, d)
	switch {
	case !diag.Daemon:
		return errors.New(problem)
	case !diag.SignedIn:
		return errSbxSignedOut
	}
	if err := sbxOwnSettings(ctx, d, list, o.Out); err != nil {
		return err
	}
	disk, err := checkDockerDisk(o.DockerDisk)
	if err != nil {
		return err
	}
	caches := ""
	if o.Caches != "" && runtime.GOOS == "windows" {
		return errors.New(errWindowsCaches)
	}
	if o.Caches != "" {
		if err := os.MkdirAll(o.Caches, 0o755); err != nil {
			return fmt.Errorf("--caches: %w", err)
		}
		if caches, err = prepareCaches(o.Caches, o.Dir); err != nil {
			return fmt.Errorf("--caches: %w", err)
		}
	}
	old, _ := LoadFrideHost(o.Dir)
	state := FrideHostState{FromCore: true}
	if !o.NoBuild {
		if state, err = o.buildTemplate(ctx, d); err != nil {
			return err
		}
	}
	state.DockerDisk, state.Caches, state.SetUp = disk, caches, time.Now().UTC().Truncate(time.Second)
	if err := saveFrideHost(o.Dir, state); err != nil {
		return err
	}
	if o.ReserveMB > 0 {
		cfg.FrideHost = &FrideHostConfig{ReserveMB: o.ReserveMB}
		if err := SaveConfig(o.Dir, cfg); err != nil {
			return err
		}
	}
	what := fmt.Sprintf("the template %s (FrIDE %s)", state.Template, short(state.FrideCommit))
	if state.FromCore {
		what = "the template the core publishes (" + nodeproto.FrideSandboxImage + ", which sbx pulls at an instance's first start)"
	}
	fmt.Fprintf(o.Out, "Set up FrIDE instances on this machine: %s, a %s Docker disk per instance, %s.\n",
		what, disk, yesNo(caches != "", "Go and npm caches in "+caches+" (a folder per instance)", "no shared caches"))
	if old != nil && old.Template != "" && old.Template != state.Template {
		fmt.Fprintf(o.Out, "Instances get the new template when the node makes their sandbox anew: at their next start while they are stopped. The old one stays in sbx: sbx template rm %s\n", old.Template)
	}
	fmt.Fprintf(o.Out, "The node's agent offers fride_host to the core within a minute. On the core, approve it once (!approve in your chat, the Nodes page, or fridolin nodes approve ID --commands).\n")
	return nil
}

// buildTemplate builds the template with FrIDE at the commit the
// checkout pins, unless sbx has it, and returns what setup records of
// it.
func (o FrideHostSetupOptions) buildTemplate(ctx context.Context, d sbxDriver) (FrideHostState, error) {
	var none FrideHostState
	commit, err := FridePin(o.Repo)
	if err != nil {
		return none, err
	}
	frideRepo := o.FrideRepo
	if frideRepo == "" {
		if o.Git == "" {
			return none, errors.New("FrIDE's git URL is missing: pass --fride-repo")
		}
		c := exec.CommandContext(ctx, o.Git, "-C", o.Repo, "remote", "get-url", "origin")
		c.Env = append(c.Environ(), "GIT_TERMINAL_PROMPT=0")
		origin, err := c.Output()
		if err != nil {
			return none, fmt.Errorf("fridolin's origin in %s: %w (pass --fride-repo)", o.Repo, err)
		}
		if frideRepo, err = frideRepoFrom(string(origin)); err != nil {
			return none, err
		}
	}
	if err := checkCloneURL(frideRepo); err != nil {
		return none, fmt.Errorf("--fride-repo: %w", err)
	}
	companion, err := companionConfig(o.Repo)
	if err != nil {
		return none, err
	}
	tag, err := FrideTemplateTag(o.Repo, frideRepo)
	if err != nil {
		return none, err
	}
	if err := o.ensureTemplate(ctx, d, tag, frideRepo); err != nil {
		return none, err
	}
	return FrideHostState{Template: tag, FrideRepo: frideRepo, FrideCommit: commit, Companion: companion}, nil
}

// errWindowsCaches refuses shared caches on Windows: where sbx puts a
// Windows folder inside the VM, which the caches' variables must name,
// is not settled yet.
const errWindowsCaches = "shared Go and npm caches are not supported on Windows yet (where sbx mounts a Windows folder in the VM is not settled): set up with --no-caches; each instance keeps its caches on its own disk"

// short is a commit's first 12 characters.
func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// yesNo is yes when b, else no.
func yesNo(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// FrideHostReport is what `fridolin node fride host status` says.
type FrideHostReport struct {
	// Host is fride-host.json, nil before setup.
	Host *FrideHostState
	// Declared is what the agent declares now; nil when it does not.
	Declared *nodeproto.FrideHostCap
}

// FrideHostStatus looks at fride_host on the node in dir with the sbx at
// sbx ("" for none), as the agent would declare it.
func FrideHostStatus(ctx context.Context, dir, sbx string) (FrideHostReport, error) {
	var r FrideHostReport
	cfg, err := LoadConfig(dir)
	if err != nil {
		return r, err
	}
	if r.Host, err = LoadFrideHost(dir); err != nil {
		return r, err
	}
	a := &Agent{Dir: dir, Config: cfg, SandboxSbx: sbx, Logf: func(string, ...any) {}}
	r.Declared = a.frideHostRead(ctx)
	return r, nil
}
