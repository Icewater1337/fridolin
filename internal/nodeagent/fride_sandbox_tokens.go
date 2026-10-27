package nodeagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Per-host tokens of a sandbox (docs/NODES.md, "GitHub and other git
// hosts"): a token for a git host never enters the VM. The VM gets a
// placeholder, and sbx's proxy puts the token in its place in every
// request to the host: in an Authorization header (Bearer, token) and in
// git's Basic auth. The token waits on the host in a file only the node
// reads, which sbx reads (`--command`) when it sets the secret and when
// it refreshes it.
//
// setHostToken and removeHostToken are the helpers; they work for any
// host (github.com, gitlab.example.com:8443). For github.com the sandbox
// also gets sbx's own github service, which puts the token into every
// request to GitHub's hosts, so that gh and git work without the
// placeholder too.

// sbxTokensDir is where a state dir keeps its sandboxes' host tokens:
// <dir>/sbx-tokens/<sandbox>/<host>.token and .placeholder, 0600.
const sbxTokensDir = "sbx-tokens"

// hostTokenRe is a host with an optional port, lower case: what a token
// is for.
var hostTokenRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*(:[0-9]{1,5})?$`)

// brainSandboxNameRe is a brain's sandbox's name: one path element.
var brainSandboxNameRe = regexp.MustCompile(`^fridolin-[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// githubHost is the host whose token also goes to sbx's github service.
const githubHost = "github.com"

// hostTokenFiles are host's token and placeholder files for sandbox.
func hostTokenFiles(dir, sandbox, host string) (token, placeholder string, err error) {
	if !hostTokenRe.MatchString(host) || len(host) > 253 {
		return "", "", fmt.Errorf("%q is not a host (like github.com or gitlab.example.com:8443)", host)
	}
	if !frideSandboxNameRe.MatchString(sandbox) && !brainSandboxNameRe.MatchString(sandbox) {
		return "", "", fmt.Errorf("%q is not a sandbox of fridolin's", sandbox)
	}
	base := filepath.Join(dir, sbxTokensDir, sandbox, strings.ReplaceAll(host, ":", "_"))
	if strings.Contains(base, "'") {
		return "", "", fmt.Errorf("the state dir %s has a ' in its path, which sbx's command cannot quote", dir)
	}
	return base + ".token", base + ".placeholder", nil
}

// CheckHostToken is token without what a copy adds around it (spaces,
// line ends, a byte order mark), or why it cannot be one.
func CheckHostToken(token string) (string, error) {
	tok := strings.TrimSpace(strings.TrimPrefix(token, string(rune(0xfeff))))
	switch {
	case tok == "":
		return "", errors.New("no token")
	case len(tok) > 4096:
		return "", errors.New("that is too long for a token")
	case strings.IndexFunc(tok, func(r rune) bool { return r <= ' ' || r > '~' }) >= 0:
		return "", errors.New("that has characters a token never has (spaces, line breaks, non-ASCII)")
	}
	return tok, nil
}

// hostPort is host's host name and its egress rule (port 443 unless it
// names one).
func hostPort(host string) (name, rule string) {
	if name, _, ok := strings.Cut(host, ":"); ok {
		return name, host
	}
	return host, host + ":443"
}

// newPlaceholder is a placeholder no request has by chance.
func newPlaceholder() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "fridolin-git-" + hex.EncodeToString(b), nil
}

// gitUser is the user name git sends with a token to host: GitHub's
// convention, else GitLab's (which takes any name with a token).
func gitUser(host string) string {
	if host == githubHost {
		return "x-access-token"
	}
	return "oauth2"
}

// setHostToken gives sandbox token for host and returns the placeholder
// that stands for it in the VM: the token in a 0600 file in dir, a
// sandbox-scoped sbx custom secret that swaps the placeholder for it in
// requests to host (for github.com, also to api.github.com), the egress
// to host, and git's credential helper for https://host in the VM, which
// hands git the placeholder. For github.com, sbx's github service gets
// the token too, on stdin. Set again, it keeps the placeholder and
// replaces the token. The token is never an argument, printed or logged.
func setHostToken(ctx context.Context, d sbxDriver, dir, sandbox, host, token string) (string, error) {
	tok, err := CheckHostToken(token)
	if err != nil {
		return "", err
	}
	tokFile, phFile, err := hostTokenFiles(dir, sandbox, host)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(tokFile), 0o700); err != nil {
		return "", err
	}
	ph, err := readPlaceholder(phFile)
	if err != nil {
		return "", err
	}
	if ph == "" {
		if ph, err = newPlaceholder(); err != nil {
			return "", err
		}
		if err := writeFile(phFile, []byte(ph+"\n"), 0o600); err != nil {
			return "", err
		}
	}
	if err := writeFile(tokFile, []byte(tok), 0o600); err != nil {
		return "", err
	}
	// redact keeps the token out of what sbx says, should it ever say it.
	redact := func(err error, stderr []byte) error {
		return errors.New(strings.ReplaceAll(sbxError(err, stderr).Error(), tok, "…"))
	}
	name, rule := hostPort(host)
	if !slices.Contains(frideSandboxEgress, rule) {
		if _, stderr, err := d.run(ctx, nil, nil, "policy", "allow", "network", "--sandbox", sandbox, rule); err != nil {
			return "", fmt.Errorf("sbx could not allow %s: %w", rule, redact(err, stderr))
		}
	}
	args := []string{"secret", "set-custom", "--sandbox", sandbox, "--host", name}
	if host == githubHost {
		args = append(args, "--host", "api.github.com")
	}
	args = append(args, "--placeholder", ph, "--command", "/bin/cat '"+tokFile+"'")
	if _, stderr, err := d.run(ctx, nil, nil, args...); err != nil {
		return "", fmt.Errorf("sbx could not set the secret for %s: %w", host, redact(err, stderr))
	}
	if host == githubHost {
		// --force replaces the secret set before, which sbx otherwise asks about.
		if _, stderr, err := d.run(ctx, strings.NewReader(tok), nil, "secret", "set", "github", "--sandbox", sandbox, "--force"); err != nil {
			return "", fmt.Errorf("sbx could not set its github secret: %w", redact(err, stderr))
		}
	}
	script := frideSandboxScript(frideSandboxCredential, "https://"+host, gitUser(host), ph)
	if _, stderr, err := d.run(ctx, nil, nil, append([]string{"exec", sandbox}, script...)...); err != nil {
		return "", fmt.Errorf("git's credential helper in the sandbox: %w", redact(err, lastLineOf(stderr)))
	}
	return ph, nil
}

// readPlaceholder is the placeholder in file, "" when there is none.
func readPlaceholder(file string) (string, error) {
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	ph := strings.TrimSpace(string(b))
	if err == nil && !strings.HasPrefix(ph, "fridolin-git-") {
		err = fmt.Errorf("%s holds no placeholder", file)
	}
	return ph, err
}

// removeHostToken takes host's token from sandbox: its secret (and sbx's
// github service for github.com), the egress setHostToken added, git's
// helper in the VM while the sandbox runs, and the files. A sandbox sbx
// no longer has lost its secrets and rules with it.
func removeHostToken(ctx context.Context, d sbxDriver, dir, sandbox, host string) error {
	tokFile, phFile, err := hostTokenFiles(dir, sandbox, host)
	if err != nil {
		return err
	}
	ph, err := readPlaceholder(phFile)
	if err != nil {
		return err
	}
	status, err := sbxSandboxStatus(ctx, d, sandbox)
	if err != nil {
		return err
	}
	var errs []error
	if status != "" {
		// What `sbx secret rm` prints is never shown: it prints part of a value.
		if ph != "" {
			if _, _, err := d.run(ctx, nil, nil, "secret", "rm", "--placeholder", ph, "--sandbox", sandbox, "-f"); err != nil {
				errs = append(errs, fmt.Errorf("sbx could not remove the secret for %s", host))
			}
		}
		if host == githubHost {
			if _, _, err := d.run(ctx, nil, nil, "secret", "rm", "github", "--sandbox", sandbox, "-f"); err != nil {
				errs = append(errs, errors.New("sbx could not remove its github secret"))
			}
		}
		if _, rule := hostPort(host); !slices.Contains(frideSandboxEgress, rule) {
			if _, stderr, err := d.run(ctx, nil, nil, "policy", "rm", "network", "--sandbox", sandbox, "--resource", rule, "--force"); err != nil {
				errs = append(errs, fmt.Errorf("sbx could not remove the rule for %s: %w", rule, sbxError(err, stderr)))
			}
		}
	}
	if status == "running" {
		script := frideSandboxScript(frideSandboxUncredential, "https://"+host)
		if _, stderr, err := d.run(ctx, nil, nil, append([]string{"exec", sandbox}, script...)...); err != nil {
			errs = append(errs, fmt.Errorf("git's credential helper in the sandbox: %w", sbxError(err, lastLineOf(stderr))))
		}
	}
	for _, f := range []string{tokFile, phFile} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// hostTokenHosts are the hosts sandbox has tokens for in dir.
func hostTokenHosts(dir, sandbox string) []string {
	files, _ := filepath.Glob(filepath.Join(dir, sbxTokensDir, sandbox, "*.placeholder"))
	var hosts []string
	for _, f := range files {
		host := strings.ReplaceAll(strings.TrimSuffix(filepath.Base(f), ".placeholder"), "_", ":")
		if hostTokenRe.MatchString(host) {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// reapplyHostTokens gives a sandbox made anew the tokens of the one
// before: sbx removed its secrets and rules with it.
func reapplyHostTokens(ctx context.Context, d sbxDriver, dir, sandbox string) error {
	for _, host := range hostTokenHosts(dir, sandbox) {
		tokFile, _, err := hostTokenFiles(dir, sandbox, host)
		if err != nil {
			return err
		}
		tok, err := os.ReadFile(tokFile)
		if err != nil {
			return fmt.Errorf("the token for %s: %w", host, err)
		}
		if _, err := setHostToken(ctx, d, dir, sandbox, host, string(tok)); err != nil {
			return err
		}
	}
	return nil
}

// sbxSandboxStatus is sbx's status of the sandbox name, "" for none.
func sbxSandboxStatus(ctx context.Context, d sbxDriver, name string) (string, error) {
	out, stderr, err := d.run(ctx, nil, nil, "ls", "--json")
	list, ok := parseSbxList(out)
	if err != nil || !ok {
		return "", fmt.Errorf("sbx does not list its sandboxes: %s", orUnknownErr(firstLine(stderr), err))
	}
	for _, s := range list {
		if s.Name == name {
			return orUnknown(s.Status), nil
		}
	}
	return "", nil
}
