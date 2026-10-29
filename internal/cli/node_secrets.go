package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"strings"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
)

// nodeSecretsOpen opens the store fridolin node secrets keeps the
// node's secrets in, for the state dir, and says where it is.
type nodeSecretsOpen func(dir string) (hostsecrets.Store, string, error)

// systemNodeSecrets is this machine's: where the node's agent reads
// FrIDE's Claude token (nodeagent.FrideSecretStore).
func systemNodeSecrets(dir string) (hostsecrets.Store, string, error) {
	home, _ := os.UserHomeDir()
	return nodeagent.FrideSecretStore(runtime.GOOS, os.Getenv, home, dir)
}

// nodeSecrets runs `fridolin node secrets`: list, or set the Claude
// token this node's FrIDE instances log in with (docs/NODES.md, "The
// Claude login"), typed without echo or read from stdin.
func nodeSecrets(pos []string, replace bool, dir string, open nodeSecretsOpen, pkg appPackage, t terminalIO, stdin io.Reader, stdout, stderr io.Writer) int {
	sub := "list"
	if len(pos) > 0 {
		sub, pos = pos[0], pos[1:]
	}
	switch {
	case sub == "list" && len(pos) == 0, sub == "set" && len(pos) == 1:
	default:
		fmt.Fprintln(stderr, "usage: fridolin node secrets [list] | fridolin node secrets set claude.oauth.token [--replace]")
		return 2
	}
	store, place, err := open(dir)
	if err != nil {
		fmt.Fprintf(stderr, "fridolin node secrets: %v%s\n", err, deniedHint(err))
		return 1
	}
	if sub == "list" {
		fmt.Fprintf(stdout, "Secrets of this node, in %s:\n", place)
		for _, name := range nodeagent.NodeSecrets {
			state := "stored"
			switch ok, err := store.Has(name); {
			case err != nil:
				state = "error: " + err.Error()
			case !ok:
				state = "missing"
			}
			fmt.Fprintf(stdout, "  %-8s %s\n", state, strings.TrimPrefix(name, "fridolin."))
		}
		fmt.Fprintln(stdout, "Store one with: fridolin node secrets set claude.oauth.token (the token claude setup-token prints, typed without echo)")
		return 0
	}
	name := pos[0]
	if !strings.HasPrefix(name, "fridolin.") {
		name = "fridolin." + name
	}
	if name != hostsecrets.ClaudeOAuthToken {
		fmt.Fprintf(stderr, "fridolin node secrets set: a node keeps only claude.oauth.token, not %s\n", pos[0])
		return 2
	}
	if err := pkg.writeError(dir); err != nil {
		fmt.Fprintf(stderr, "fridolin node secrets set: %v\n", err)
		return 1
	}
	have, err := store.Has(name)
	if err != nil && !errors.Is(err, hostsecrets.ErrNotFound) {
		fmt.Fprintf(stderr, "fridolin node secrets set: %v%s\n", err, deniedHint(err))
		return 1
	}
	if have && !replace {
		fmt.Fprintf(stderr, "fridolin node secrets set: claude.oauth.token is already stored in %s; pass --replace to replace it\n", place)
		return 1
	}
	var value string
	if t.readHidden != nil {
		value, err = t.readHidden("The token claude setup-token printed (typing is not shown): ")
	} else {
		var b []byte
		b, err = io.ReadAll(io.LimitReader(stdin, 64<<10))
		value = string(b)
	}
	if err != nil {
		fmt.Fprintf(stderr, "fridolin node secrets set: reading the token: %v\n", err)
		return 1
	}
	tok, err := nodeagent.CheckClaudeToken(value)
	if err != nil {
		fmt.Fprintf(stderr, "fridolin node secrets set: %v; nothing was stored\n", err)
		return 1
	}
	if err := store.Set(name, tok); err != nil {
		fmt.Fprintf(stderr, "fridolin node secrets set: %v%s\n", err, deniedHint(err))
		return 1
	}
	fmt.Fprintf(stdout, "Stored claude.oauth.token in %s.\nThe agent sees it within a minute: the core then logs in the FrIDE instances running here, and every one started from now on.\n", place)
	return 0
}

// deniedHint is what to do when the state dir refused the command.
func deniedHint(err error) string {
	if runtime.GOOS == "windows" && errors.Is(err, fs.ErrPermission) {
		return " (run it in an administrator PowerShell)"
	}
	return ""
}
