package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// frideSandboxWait is how long start waits for FrIDE to answer.
const frideSandboxWait = 3 * time.Minute

// FrideSandboxUpdate builds FrIDE again in the sandbox at the commit the
// checkout pins, and has the agent start the new one when FrIDE runs.
// The sandbox, its workspaces and FrIDE's config stay.
func FrideSandboxUpdate(ctx context.Context, o FrideSandboxOptions) error {
	st, err := o.loadMade()
	if err != nil {
		return err
	}
	commit, err := FridePin(o.Repo)
	if err != nil {
		return err
	}
	if status, err := o.sandboxStatus(ctx); err != nil {
		return err
	} else if status == "" {
		return fmt.Errorf("sbx has no sandbox %s any more: fridolin node fride sandbox rm, then create", FrideSandboxName)
	}
	if tag, err := FrideSandboxTemplateTag(o.Repo); err == nil && tag != st.Template {
		fmt.Fprintf(o.Out, "Note: the template changed (%s, the sandbox has %s). update builds FrIDE only; for the new template, rm and create the sandbox (its workspaces go with it).\n", tag, st.Template)
	}
	if o.FrideRepo != "" {
		st.Repo = o.FrideRepo
	}
	if o.GitName != "" || o.GitEmail != "" {
		if err := o.gitIdentity(ctx); err != nil {
			return err
		}
	}
	if err := o.build(ctx, st, commit); err != nil {
		return err
	}
	if err := saveFrideSandbox(o.Dir, *st); err != nil {
		return err
	}
	if st.Run {
		if err := o.exec(ctx, nil, frideSandboxStop); err != nil {
			return err
		}
		fmt.Fprintln(o.Out, "Stopped the old FrIDE; the node's agent starts the new one within seconds.")
	}
	return nil
}

// FrideSandboxStart has the agent keep FrIDE running, waits until FrIDE
// answers and pairs the node with it unless it is paired. It returns
// what FrIDE reports.
func FrideSandboxStart(ctx context.Context, o FrideSandboxOptions) (FrideInfo, error) {
	cfg, err := LoadConfig(o.Dir)
	if err != nil {
		return FrideInfo{}, err
	}
	if err := checkFrideSandboxPair(cfg, o.frideURL()); err != nil {
		return FrideInfo{}, err
	}
	st, err := o.loadMade()
	if err != nil {
		return FrideInfo{}, err
	}
	if !st.Run {
		st.Run = true
		if err := saveFrideSandbox(o.Dir, *st); err != nil {
			return FrideInfo{}, err
		}
	}
	fmt.Fprintf(o.Out, "The node's agent keeps FrIDE running now; waiting for it at %s…\n", o.frideURL())
	if err := waitAnswers(ctx, o.frideURL(), o.wait); err != nil {
		return FrideInfo{}, fmt.Errorf("%w: does this node's agent run (fridolin node status --state-dir %q)? It starts FrIDE within seconds", err, o.Dir)
	}
	if cfg.Fride != nil {
		if tok, err := readFrideToken(o.Dir); err == nil {
			if info, err := ProbeFride(ctx, cfg.Fride.URL, tok); err == nil {
				return info, nil
			}
		}
	}
	code, err := o.pairCode(ctx)
	if err != nil {
		return FrideInfo{}, err
	}
	return PairFride(ctx, FridePairOptions{Dir: o.Dir, URL: o.frideURL(), Code: code,
		Webhook: "127.0.0.1:" + strconv.Itoa(FrideSandboxWebhookPort), Base: st.Base})
}

// waitAnswers waits until something answers HTTP at url.
func waitAnswers(ctx context.Context, url string, wait time.Duration) error {
	if wait <= 0 {
		wait = frideSandboxWait
	}
	deadline := time.Now().Add(wait)
	for {
		rctx, cancel := context.WithTimeout(ctx, frideProbeTimeout)
		req, err := http.NewRequestWithContext(rctx, http.MethodGet, url+"/", nil)
		if err != nil {
			cancel()
			return err
		}
		resp, err := frideClient.Do(req)
		cancel()
		if err == nil {
			resp.Body.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("FrIDE does not answer at %s after %s", url, wait.Round(time.Second))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// pairCode is a pairing code from FrIDE in the sandbox. It is never
// printed or logged.
func (o FrideSandboxOptions) pairCode(ctx context.Context) (string, error) {
	out, stderr, err := o.sbx().run(ctx, nil, nil, append([]string{"exec", FrideSandboxName}, frideSandboxScript(frideSandboxPairCode)...)...)
	if err != nil {
		return "", fmt.Errorf("fride pair-companion in the sandbox: %w", sbxError(err, stderr))
	}
	var code struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(out, &code) != nil || code.Code == "" {
		return "", errors.New("fride pair-companion in the sandbox printed no code")
	}
	return code.Code, nil
}

// FrideSandboxStop has the agent stop keeping FrIDE running, and ends it.
func FrideSandboxStop(ctx context.Context, o FrideSandboxOptions) error {
	st, err := o.loadMade()
	if err != nil {
		return err
	}
	st.Run = false
	if err := saveFrideSandbox(o.Dir, *st); err != nil {
		return err
	}
	// A stopped sandbox has no FrIDE running, and an exec would boot it.
	if status, err := o.sandboxStatus(ctx); err != nil || status != "running" {
		return err
	}
	return o.exec(ctx, nil, frideSandboxStop)
}

// FrideSandboxRemove removes the sandbox, with its workspaces, and the
// state; the base folder and the node's pairing stay.
func FrideSandboxRemove(ctx context.Context, o FrideSandboxOptions) error {
	if st, err := LoadFrideSandbox(o.Dir); err == nil && st != nil && st.Run {
		st.Run = false
		if err := saveFrideSandbox(o.Dir, *st); err != nil {
			return err
		}
	}
	status, err := o.sandboxStatus(ctx)
	if err != nil {
		return err
	}
	if status != "" {
		if _, stderr, err := o.sbx().run(ctx, nil, nil, "rm", "-f", FrideSandboxName); err != nil {
			return fmt.Errorf("sbx could not remove %s: %w", FrideSandboxName, sbxError(err, stderr))
		}
	}
	return removeFrideSandbox(o.Dir)
}

// FrideSandboxLogin stores the Claude token for FrIDE's agents in the
// sandbox. The token goes there on stdin, never as an argument.
func FrideSandboxLogin(ctx context.Context, o FrideSandboxOptions, token string) error {
	tok, err := CheckClaudeToken(token)
	if err != nil {
		return err
	}
	if _, err := o.loadMade(); err != nil {
		return err
	}
	if _, stderr, err := o.sbx().run(ctx, strings.NewReader(tok+"\n"), nil,
		append([]string{"exec", "-i", FrideSandboxName}, frideSandboxScript(frideSandboxLogin)...)...); err != nil {
		return fmt.Errorf("storing the token in the sandbox: %w", sbxError(err, lastLineOf(stderr)))
	}
	return nil
}

// FrideSandboxSetupToken is `claude setup-token` in the sandbox, on the
// terminal of the person who runs it.
func FrideSandboxSetupToken(o FrideSandboxOptions) *exec.Cmd {
	c := exec.Command(o.Sbx, "exec", "-it", FrideSandboxName, "claude", "setup-token")
	c.Env = append(sbxEnv(c.Environ()), o.env...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c
}

// CheckClaudeToken is token as `claude setup-token` prints it, without
// what a copy or Windows PowerShell adds (a byte order mark, carriage
// returns, spaces), or why it is not one.
func CheckClaudeToken(token string) (string, error) {
	tok := strings.TrimPrefix(token, string(rune(0xfeff)))
	tok = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' || r == ' ' {
			return -1
		}
		return r
	}, tok)
	switch {
	case tok == "":
		return "", errors.New("no token: run claude setup-token and paste the token it prints")
	case strings.Contains(tok, "#"):
		return "", errors.New("that is the sign-in code from the browser, not the token claude setup-token prints after it")
	case !strings.HasPrefix(tok, "sk-ant-oat") || len(tok) == len("sk-ant-oat"):
		return "", errors.New("that does not start with sk-ant-oat, as a token from claude setup-token does")
	case strings.IndexFunc(tok, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) >= 0:
		return "", errors.New("that has characters a token never has (copied with other text?)")
	}
	return tok, nil
}
