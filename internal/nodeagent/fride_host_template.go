package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// The FrIDE template (docs/NODES.md, "FrIDE instances"): the image every
// instance's sandbox is made from, built with Docker from
// container/sandbox/fride.Dockerfile in a fridolin checkout and loaded
// into sbx's own image store. It holds FrIDE, built at the commit the
// checkout pins, Claude Code and ponytail, and no secret.
const (
	// frideTemplateRepo is the template's image name.
	frideTemplateRepo = "fridolin-fride-sbx"
	// frideTemplateDockerfile is the template's Dockerfile in a checkout.
	frideTemplateDockerfile = "container/sandbox/fride.Dockerfile"
	// frideImageDockerfile pins FrIDE's commit (container/fride).
	frideImageDockerfile = "container/fride/Dockerfile"
	// frideCompanionConfig is the box's FrIDE config, which an
	// instance's starts from.
	frideCompanionConfig = "container/fride/companion.json"
)

// commitRe is a full git commit id.
var commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// FridePin is the FrIDE commit the checkout at repo pins: the template's
// FRIDE_COMMIT, which a test holds to the fridolin-fride image's.
func FridePin(repo string) (string, error) {
	return dockerfileArg(filepath.Join(repo, filepath.FromSlash(frideTemplateDockerfile)), "FRIDE_COMMIT", commitRe)
}

// dockerfileArg is the default of ARG name in the Dockerfile at path,
// which must match re.
func dockerfileArg(path, name string, re *regexp.Regexp) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("not a fridolin checkout: %w", err)
	}
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ARG "+name+"="); ok {
			if !re.MatchString(v) {
				return "", fmt.Errorf("%s: ARG %s=%q is not what it should be", path, name, v)
			}
			return v, nil
		}
	}
	return "", fmt.Errorf("%s names no ARG %s", path, name)
}

// FrideTemplateTag is the template's name for the checkout at repo and
// FrIDE's git URL: a hash of the Dockerfile, which pins all that goes in,
// and of the URL.
func FrideTemplateTag(repo, frideRepo string) (string, error) {
	b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(frideTemplateDockerfile)))
	if err != nil {
		return "", fmt.Errorf("%s is not a fridolin checkout: %w", repo, err)
	}
	sum := sha256.Sum256(append(append(b, 0), frideRepo...))
	return frideTemplateRepo + ":" + hex.EncodeToString(sum[:])[:12], nil
}

// githubRepoRe is a GitHub repository's URL, https or ssh.
var githubRepoRe = regexp.MustCompile(`^(?:https://(?:[^@/]+@)?github\.com/|ssh://git@github\.com/|git@github\.com:)([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

// frideRepoFrom is FrIDE's https URL beside fridolin's at origin: the
// same owner on GitHub.
func frideRepoFrom(origin string) (string, error) {
	m := githubRepoRe.FindStringSubmatch(strings.TrimSpace(origin))
	if m == nil {
		return "", fmt.Errorf("fridolin's origin %q is not on GitHub: pass --fride-repo", origin)
	}
	return "https://github.com/" + m[1] + "/fride", nil
}

// companionConfig is the box's FrIDE config from the checkout, for an
// instance: its name; the prepare script sets the VM's base folder and
// workspaces.
func companionConfig(repo string) (json.RawMessage, error) {
	b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(frideCompanionConfig)))
	if err != nil {
		return nil, fmt.Errorf("not a fridolin checkout: %w", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", frideCompanionConfig, err)
	}
	cfg["name"] = "fridolin companion (instance)"
	return json.MarshalIndent(cfg, "", "  ")
}

// runProg runs a program of the setup (Docker), its output to Out.
func (o FrideHostSetupOptions) runProg(ctx context.Context, prog string, args ...string) error {
	c := exec.CommandContext(ctx, prog, args...)
	c.Env = slices.Concat(sbxEnv(c.Environ()), o.env)
	c.Stdin = strings.NewReader("")
	c.Stdout, c.Stderr = o.Out, o.Out
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", filepath.Base(prog), args[0], err)
	}
	return nil
}

// ensureTemplate builds the template tag with FrIDE from frideRepo and
// loads it into sbx, unless sbx has it.
func (o FrideHostSetupOptions) ensureTemplate(ctx context.Context, d sbxDriver, tag, frideRepo string) error {
	if sbxHasTemplate(ctx, d, tag) {
		fmt.Fprintf(o.Out, "sbx has the template %s already.\n", tag)
		return nil
	}
	if o.Docker == "" {
		return fmt.Errorf("docker is not installed: the template %s is built with Docker (Docker Desktop or OrbStack)", tag)
	}
	dockerfile := filepath.Join(o.Repo, filepath.FromSlash(frideTemplateDockerfile))
	fmt.Fprintf(o.Out, "Building the template %s with Docker: FrIDE from %s, Claude Code, ponytail (a few minutes the first time)…\n", tag, frideRepo)
	if err := o.runProg(ctx, o.Docker, "build", "-f", dockerfile, "--build-arg", "FRIDE_REPO="+frideRepo, "-t", tag, o.Repo); err != nil {
		return err
	}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return err
	}
	tar := filepath.Join(o.Dir, "template.tar")
	defer os.Remove(tar)
	if err := o.runProg(ctx, o.Docker, "save", tag, "-o", tar); err != nil {
		return err
	}
	fmt.Fprintln(o.Out, "Loading it into sbx…")
	if _, stderr, err := d.run(ctx, nil, nil, "template", "load", tar); err != nil {
		return fmt.Errorf("sbx template load: %w", sbxError(err, stderr))
	}
	if !sbxHasTemplate(ctx, d, tag) {
		return fmt.Errorf("sbx loaded the image, but does not list the template %s", tag)
	}
	// sbx keeps its own copy; Docker's is not needed (several GB).
	if err := o.runProg(ctx, o.Docker, "image", "rm", tag); err != nil {
		fmt.Fprintf(o.Out, "Note: could not remove Docker's copy of %s: %v\n", tag, err)
	}
	return nil
}
