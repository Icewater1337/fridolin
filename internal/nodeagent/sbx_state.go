package nodeagent

import (
	"context"
	"errors"
)

// TestedSbxVersion is the sbx release fridolin's sandboxes were tested
// with; fride host setup notes another.
const TestedSbxVersion = "v0.50.0"

// errSbxSignedOut is sbx without Docker's sign-in.
var errSbxSignedOut = errors.New("sbx is not signed in to Docker: run sbx login, then the command again")

// sbx is the driver of sbx, nil without one. On Windows a service has
// one without a path: it looks for the signed-in user's sbx with each
// call.
func (a *Agent) sbx() sbxDriver {
	if a.SandboxDriver != nil {
		return a.SandboxDriver
	}
	if a.SandboxSbx == "" && !sbxInSession() {
		return nil
	}
	return sbxCLI{path: a.SandboxSbx, self: a.Executable}
}

// sbxHasTemplate reports whether sbx's image store holds the template.
func sbxHasTemplate(ctx context.Context, d sbxDriver, tpl string) bool {
	out, _, err := d.run(ctx, nil, nil, "template", "ls", "--json")
	return err == nil && sbxListsTemplate(out, tpl)
}

// sbxState is the daemon and Docker's sign-in, as `sbx diagnose --json`
// says, and the sandboxes from `sbx ls --json`. problem says why sbx
// cannot make or run sandboxes, "" when it can.
func sbxState(ctx context.Context, d sbxDriver) (sbxDiagnosis, []sbxSandbox, string) {
	// diagnose exits non-zero when a check fails, and still prints them.
	dout, _, _ := d.run(ctx, nil, nil, "diagnose", "--json")
	diag, known := parseSbxDiagnose(dout)
	out, stderr, err := d.run(ctx, nil, nil, "ls", "--json")
	list, parsed := parseSbxList(out)
	if !known {
		// An sbx without diagnose: what ls says.
		diag = sbxDiagnosis{Daemon: err == nil || sbxSaysSignIn(firstLine(stderr)), SignedIn: err == nil}
	}
	switch {
	case !diag.Daemon:
		return diag, list, "sbx's daemon does not answer: " + orUnknownErr(firstLine(stderr), err)
	case !diag.SignedIn:
		return diag, list, "sbx is not signed in to Docker: run sbx login on this machine"
	case err != nil:
		return diag, list, "sbx ls: " + orUnknownErr(firstLine(stderr), err)
	case !parsed:
		return diag, list, "sbx ls --json printed what this node does not read"
	}
	return diag, list, ""
}

func orUnknownErr(msg string, err error) string {
	if msg != "" {
		return msg
	}
	if err != nil {
		return err.Error()
	}
	return "no answer"
}
