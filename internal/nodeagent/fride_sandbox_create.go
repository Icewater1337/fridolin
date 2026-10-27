package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// FrideSandboxOptions are what `fridolin node fride sandbox` works with.
type FrideSandboxOptions struct {
	// Dir is the node's state dir: the sandboxed FrIDE's own node.
	Dir string
	// Sbx, Docker and Git are the programs; Git "" makes create clone no
	// starter repositories and FrideRepo necessary.
	Sbx, Docker, Git string
	// Repo is a fridolin checkout: the template's Dockerfile, FrIDE's
	// pinned commit and the companion config (create, update).
	Repo string
	// Base is the host folder for FrIDE's repositories, mounted read-write
	// at its own path (create).
	Base string
	// FrideRepo is FrIDE's git URL ("" on create: Repo's origin with
	// fridolin replaced by fride; on update: the one created with).
	FrideRepo string
	// GitName and GitEmail are the identity FrIDE's agents commit with.
	GitName, GitEmail string
	// DockerDisk is the size of the sandbox's /var/lib/docker ("":
	// DefaultFrideSandboxDockerDisk); Caches the host folder of the
	// shared Go and npm caches ("": none) (create).
	DockerDisk, Caches string
	// Out gets what is done, for the person who runs it.
	Out io.Writer
	// env is added to each program's environment (tests: the fakes');
	// url is FrIDE's address and wait how long start waits for it; name
	// is the sandbox, FrideSandboxName unless a test names another
	// (tests).
	env  []string
	url  string
	wait time.Duration
	name string
}

func (o FrideSandboxOptions) sbx() sbxCLI { return sbxCLI{path: o.Sbx, env: o.env} }

// sandbox is the sandbox's name.
func (o FrideSandboxOptions) sandbox() string {
	if o.name != "" {
		return o.name
	}
	return FrideSandboxName
}

func (o FrideSandboxOptions) frideURL() string {
	if o.url != "" {
		return o.url
	}
	return frideSandboxURL
}

// exec runs script in the sandbox, its output to Out.
func (o FrideSandboxOptions) exec(ctx context.Context, stdin io.Reader, script string, args ...string) error {
	flags := []string{"exec"}
	if stdin != nil {
		flags = append(flags, "-i")
	}
	out, stderr, err := o.sbx().run(ctx, stdin, nil, slices.Concat(flags, []string{o.sandbox()}, frideSandboxScript(script, args...))...)
	for _, b := range [][]byte{out, stderr} {
		if len(b) > 0 {
			o.Out.Write(b)
		}
	}
	if err != nil {
		return fmt.Errorf("in the sandbox: %w", sbxError(err, lastLineOf(stderr)))
	}
	return nil
}

// lastLineOf is the last line of b, which says why a script failed.
func lastLineOf(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return []byte(s)
}

// FrideSandboxTemplateTag is the template's name for the checkout at
// repo: a hash of its Dockerfile, which pins all that goes in.
func FrideSandboxTemplateTag(repo string) (string, error) {
	b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(frideSandboxDockerfile)))
	if err != nil {
		return "", fmt.Errorf("%s is not a fridolin checkout: %w", repo, err)
	}
	sum := sha256.Sum256(b)
	return frideSandboxTemplateRepo + ":" + hex.EncodeToString(sum[:])[:12], nil
}

// githubRepoRe is a GitHub repository's URL, https or ssh.
var githubRepoRe = regexp.MustCompile(`^(?:https://(?:[^@/]+@)?github\.com/|ssh://git@github\.com/|git@github\.com:)([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

// frideRepoFrom is FrIDE's https URL beside fridolin's at origin: the
// same owner on GitHub. The VM reaches GitHub over https only.
func frideRepoFrom(origin string) (string, error) {
	m := githubRepoRe.FindStringSubmatch(strings.TrimSpace(origin))
	if m == nil {
		return "", fmt.Errorf("fridolin's origin %q is not on GitHub: pass --fride-repo", origin)
	}
	return "https://github.com/" + m[1] + "/fride", nil
}

// gitOut runs git with args and returns its output, trimmed.
func (o FrideSandboxOptions) gitOut(ctx context.Context, args ...string) (string, error) {
	c := exec.CommandContext(ctx, o.Git, args...)
	c.Env = append(c.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := c.Output()
	return strings.TrimSpace(string(out)), err
}

// companionConfig is the box's FrIDE config from the checkout, for the
// sandbox: its name, the workspaces in the VM and base as the only base
// folder.
func companionConfig(repo, base string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(frideCompanionConfig)))
	if err != nil {
		return nil, fmt.Errorf("not a fridolin checkout: %w", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", frideCompanionConfig, err)
	}
	cfg["name"] = "fridolin companion (sandbox)"
	cfg["workspaceRoot"] = "~/fride-companion/workspaces"
	cfg["baseFolders"] = []string{base}
	out, err := json.MarshalIndent(cfg, "", "  ")
	return append(out, '\n'), err
}

// sandboxStatus is sbx's status of the sandboxed FrIDE's sandbox: "" for
// none.
func (o FrideSandboxOptions) sandboxStatus(ctx context.Context) (string, error) {
	return sbxSandboxStatus(ctx, o.sbx(), o.sandbox())
}

// errNoFrideSandbox is a node without the sandboxed FrIDE.
var errNoFrideSandbox = errors.New("no sandboxed FrIDE on this node: fridolin node fride sandbox create")

// loadMade is the state of a sandbox create made.
func (o FrideSandboxOptions) loadMade() (*FrideSandboxState, error) {
	st, err := LoadFrideSandbox(o.Dir)
	if err == nil && st == nil {
		err = errNoFrideSandbox
	}
	return st, err
}
