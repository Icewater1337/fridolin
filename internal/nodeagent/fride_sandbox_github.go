package nodeagent

import (
	"context"
	"fmt"
)

// FrideSandboxSetHostToken gives the sandboxed FrIDE's VM a token for
// host, as setHostToken does, and returns the placeholder that stands
// for it there.
func FrideSandboxSetHostToken(ctx context.Context, o FrideSandboxOptions, host, token string) (string, error) {
	if _, err := o.loadMade(); err != nil {
		return "", err
	}
	if status, err := o.sandboxStatus(ctx); err != nil {
		return "", err
	} else if status == "" {
		return "", fmt.Errorf("sbx has no sandbox %s any more: fridolin node fride sandbox rm, then create", o.sandbox())
	}
	return setHostToken(ctx, o.sbx(), o.Dir, o.sandbox(), host, token)
}

// FrideSandboxRemoveHostToken takes host's token from the sandboxed
// FrIDE's VM and from the node, as removeHostToken does.
func FrideSandboxRemoveHostToken(ctx context.Context, o FrideSandboxOptions, host string) error {
	return removeHostToken(ctx, o.sbx(), o.Dir, o.sandbox(), host)
}

// FrideSandboxGitHub gives the sandboxed FrIDE's VM GitHub: token (the
// host's gh auth token) for github.com, through sbx's github service and
// a placeholder for git, so that FrIDE's agents push and use gh inside.
func FrideSandboxGitHub(ctx context.Context, o FrideSandboxOptions, token string) error {
	_, err := FrideSandboxSetHostToken(ctx, o, githubHost, token)
	return err
}

// FrideSandboxHosts are the hosts the sandboxed FrIDE has tokens for.
func FrideSandboxHosts(o FrideSandboxOptions) []string {
	return hostTokenHosts(o.Dir, o.sandbox())
}
