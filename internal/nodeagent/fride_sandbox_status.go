package nodeagent

import (
	"bufio"
	"bytes"
	"context"
	"strings"
)

// FrideSandboxReport is what `fridolin node fride sandbox status` says.
type FrideSandboxReport struct {
	// State is fride-sandbox.json, nil before create.
	State *FrideSandboxState
	// Sandbox is sbx's status of the sandbox, "" for none; Problem says
	// why sbx was not asked or did not answer.
	Sandbox, Problem string
	// Answers says FrIDE answers on the host; Paired that the node holds
	// a token FrIDE takes, with the repositories FrIDE reports.
	Answers, Paired bool
	Repos           []string
	// Inside is what the sandbox says while it runs: login, git, running
	// and commit (status script); nil while it is stopped.
	Inside map[string]string
}

// FrideSandboxStatus looks at the sandboxed FrIDE without starting it:
// a stopped sandbox is not booted.
func FrideSandboxStatus(ctx context.Context, o FrideSandboxOptions) (FrideSandboxReport, error) {
	var r FrideSandboxReport
	st, err := LoadFrideSandbox(o.Dir)
	if err != nil {
		return r, err
	}
	r.State = st
	if status, err := o.sandboxStatus(ctx); err != nil {
		r.Problem = err.Error()
	} else {
		r.Sandbox = status
	}
	if r.Sandbox == "running" {
		args := append([]string{"exec", o.sandbox()}, frideSandboxScript(frideSandboxStatus)...)
		if out, _, err := o.sbx().run(ctx, nil, nil, args...); err == nil {
			r.Inside = parseKeyValues(out)
		}
	}
	pctx, cancel := context.WithTimeout(ctx, frideProbeTimeout)
	defer cancel()
	r.Answers = waitAnswers(pctx, o.frideURL(), 1) == nil
	if cfg, err := LoadConfig(o.Dir); err == nil && cfg.Fride != nil && cfg.Fride.URL == o.frideURL() && r.Answers {
		if tok, err := readFrideToken(o.Dir); err == nil {
			if info, err := ProbeFride(pctx, cfg.Fride.URL, tok); err == nil {
				r.Paired, r.Repos = true, info.Repos
			}
		}
	}
	return r, nil
}

// parseKeyValues reads key=value lines.
func parseKeyValues(b []byte) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok {
			m[k] = v
		}
	}
	return m
}
