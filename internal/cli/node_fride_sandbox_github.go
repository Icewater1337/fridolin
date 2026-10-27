package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
)

// ghTokenTimeout bounds `gh auth token`.
const ghTokenTimeout = 20 * time.Second

// ghToken is the host's GitHub token from `gh auth token`, never printed:
// errNoGh when gh is not installed or not logged in.
func ghToken(ctx context.Context) (string, error) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return "", errNoGh
	}
	ctx, cancel := context.WithTimeout(ctx, ghTokenTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, gh, "auth", "token", "--hostname", "github.com").Output()
	tok := strings.TrimSpace(string(out))
	if err != nil || tok == "" {
		return "", errNoGh
	}
	return tok, nil
}

var errNoGh = errors.New("gh is not installed or not logged in to github.com (gh auth login)")

// frideSandboxGitHub is github: it gives the sandbox the host's GitHub
// token (gh auth token, or stdin with --token-stdin), or takes it away
// (--remove). The token is never an argument, printed or logged.
func frideSandboxGitHub(ctx context.Context, o nodeagent.FrideSandboxOptions, f frideSandboxFlags, stdout io.Writer, fail func(string, ...any) int) int {
	if st, err := nodeagent.LoadFrideSandbox(o.Dir); err != nil || st == nil {
		return fail("no sandboxed FrIDE here: fridolin node fride sandbox create first")
	}
	if f.remove {
		if err := nodeagent.FrideSandboxRemoveHostToken(ctx, o, "github.com"); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintln(stdout, "Took GitHub's token from the sandbox and this node.")
		return 0
	}
	var tok string
	if f.tokenStdin {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 8192))
		if err != nil {
			return fail("%v", err)
		}
		tok = string(b)
	} else {
		var err error
		if tok, err = ghToken(ctx); err != nil {
			return fail("%v; or pipe a token in with --token-stdin", err)
		}
	}
	if err := nodeagent.FrideSandboxGitHub(ctx, o, tok); err != nil {
		return fail("%v", err)
	}
	fmt.Fprintln(stdout, "Gave the sandbox GitHub: gh, and git over https, work inside; the token stays on this machine, and the proxy adds it to requests to GitHub. Run this again after gh auth refresh or a new gh login.")
	return 0
}

// frideSandboxGitHubAfterCreate gives a new sandbox GitHub when gh is
// logged in; otherwise it says how.
func frideSandboxGitHubAfterCreate(ctx context.Context, o nodeagent.FrideSandboxOptions, stdout io.Writer) {
	tok, err := ghToken(ctx)
	if err == nil {
		err = nodeagent.FrideSandboxGitHub(ctx, o, tok)
	}
	if err != nil {
		fmt.Fprintf(stdout, "Note: the sandbox has no GitHub login of this machine's (%v). Later: fridolin node fride sandbox github.\n", err)
		return
	}
	fmt.Fprintln(stdout, "Gave the sandbox GitHub with this machine's gh login (gh and git over https work inside; the token stays here).")
}
