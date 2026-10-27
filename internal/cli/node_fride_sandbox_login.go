package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
)

// frideSandboxLogin stores the Claude login of FrIDE's agents in the
// sandbox: the token claude setup-token prints, typed without echo or
// piped in; --setup-token runs claude setup-token in the sandbox first.
// The token is never an argument, printed or logged.
func frideSandboxLogin(ctx context.Context, o nodeagent.FrideSandboxOptions, f frideSandboxFlags, stdout io.Writer, fail func(string, ...any) int) int {
	if st, err := nodeagent.LoadFrideSandbox(o.Dir); err != nil || st == nil {
		return fail("no sandboxed FrIDE here: fridolin node fride sandbox create first")
	}
	term := stdinTerminal()
	if f.setupToken {
		if term.readHidden == nil {
			return fail("--setup-token needs a terminal")
		}
		fmt.Fprintln(stdout, "Running claude setup-token in the sandbox: open the link it prints, sign in, and paste the code back there.")
		if err := nodeagent.FrideSandboxSetupToken(o).Run(); err != nil {
			return fail("claude setup-token in the sandbox: %v", err)
		}
	}
	var tok string
	if term.readHidden != nil {
		var err error
		if tok, err = term.readHidden("The token claude setup-token printed (sk-ant-oat…; it is not shown): "); err != nil {
			return fail("%v", err)
		}
	} else {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			return fail("%v", err)
		}
		tok = strings.TrimSpace(string(b))
	}
	if err := nodeagent.FrideSandboxLogin(ctx, o, tok); err != nil {
		return fail("nothing stored: %v", err)
	}
	fmt.Fprintln(stdout, "Stored the Claude login for FrIDE's agents in the sandbox (its ~/.claude/settings.json, 0600). Agents started from now on use it.")
	return 0
}

// frideSandboxStatus prints the sandboxed FrIDE's status: 1 when it is
// not made, or should run and does not answer or is not paired.
func frideSandboxStatus(ctx context.Context, o nodeagent.FrideSandboxOptions, stdout io.Writer, fail func(string, ...any) int) int {
	r, err := nodeagent.FrideSandboxStatus(ctx, o)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(stdout, "State dir: %s\n", o.Dir)
	if r.Problem != "" {
		fmt.Fprintf(stdout, "sbx: %s\n", r.Problem)
	}
	if r.State == nil {
		fmt.Fprintf(stdout, "Sandbox: %s; not made (fridolin node fride sandbox create)\n", orDash(r.Sandbox))
		return 1
	}
	st := r.State
	fmt.Fprintf(stdout, "Sandbox: %s, %s (template %s)\n", st.Name, orDash(r.Sandbox), st.Template)
	fmt.Fprintf(stdout, "FrIDE: %s from %s, built %s\n", orDash(st.Commit), st.Repo, st.Built.Local().Format("2006-01-02 15:04"))
	fmt.Fprintf(stdout, "Repositories: %s\n", st.Base)
	fmt.Fprintf(stdout, "Kept running: %s\n", yesNo(st.Run, "yes", "no (fridolin node fride sandbox start)"))
	fmt.Fprintf(stdout, "Answers: %s\n", yesNo(r.Answers, "yes, at http://127.0.0.1:17850", "no"))
	fmt.Fprintf(stdout, "Paired: %s\n", yesNo(r.Paired, "yes, repositories "+orNone(r.Repos), "no (start pairs)"))
	if r.Inside == nil {
		fmt.Fprintln(stdout, "Claude login, git identity: not looked at while the sandbox is stopped")
	} else {
		fmt.Fprintf(stdout, "Claude login: %s\n", yesNo(r.Inside["login"] == "yes", "yes", "no (fridolin node fride sandbox login)"))
		fmt.Fprintf(stdout, "Git identity: %s\n", yesNo(r.Inside["git"] == "yes", "yes", "no (update --git-name NAME --git-email EMAIL)"))
	}
	if st.Run && !(r.Answers && r.Paired) {
		return 1
	}
	return 0
}

func yesNo(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}
