package nodeagent

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// An instance's network (docs/NODES.md, "What an instance gets"): on top
// of sbx's global deny-all and the hosts the Claude kit opens, each
// sandbox has rules of its own, which every start brings up to date:
// the baseline, the hosts of its git identities, and the egress the core
// gives it. downloads.claude.ai and bridge.claudeusercontent.com are
// denied when the sandbox is made.

// frideInstanceBaseline is what every instance may reach: Anthropic's
// API, Go's module proxy and checksum database, npm, and Docker Hub.
var frideInstanceBaseline = []string{
	"api.anthropic.com:443",
	"proxy.golang.org:443", "sum.golang.org:443", "storage.googleapis.com:443",
	"registry.npmjs.org:443",
	"registry-1.docker.io:443", "auth.docker.io:443", "index.docker.io:443", "production.cloudflare.docker.com:443",
}

// frideInstanceGitHub are GitHub's hosts, for an instance with a GitHub
// identity.
var frideInstanceGitHub = []string{
	"github.com:443", "api.github.com:443", "codeload.github.com:443",
	"objects.githubusercontent.com:443", "raw.githubusercontent.com:443",
}

// frideInstanceOpenAI are OpenAI's hosts, for Codex: an instance gets
// them when sbx holds the ChatGPT login Codex runs with.
var frideInstanceOpenAI = []string{"api.openai.com:443", "chatgpt.com:443", "auth.openai.com:443"}

// egressHostRe is an egress host the core may give: a host, or *. and a
// domain, with an optional port, lower case.
var egressHostRe = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*(:[0-9]{1,5})?$`)

// egressRule is host's network rule: port 443 unless it names one.
func egressRule(host string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(host))
	if !egressHostRe.MatchString(h) || len(h) > 253 {
		return "", fmt.Errorf("egress host %q is not a host (like registry.example.com, *.example.com or git.example.com:8443)", host)
	}
	if _, port, ok := strings.Cut(h, ":"); ok {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("egress host %q has no port 1 to 65535", host)
		}
		return h, nil
	}
	return h + ":443", nil
}

// isGitHub reports whether id is for GitHub.
func isGitHub(id nodeproto.FrideIdentity) bool {
	return strings.EqualFold(id.Kind, "github") || strings.EqualFold(id.Host, githubHost)
}

// frideInstanceRules are the network rules of an instance: the
// baseline, its webhook on the loopback, OpenAI's hosts for codex, the
// identities' hosts (all of GitHub's for a GitHub one) and the egress,
// sorted.
func frideInstanceRules(webhookPort int, ids []nodeproto.FrideIdentity, egress []string, codex bool) ([]string, error) {
	rules := slices.Concat(frideInstanceBaseline, []string{"localhost:" + strconv.Itoa(webhookPort)})
	if codex {
		rules = append(rules, frideInstanceOpenAI...)
	}
	for _, id := range ids {
		if isGitHub(id) {
			rules = append(rules, frideInstanceGitHub...)
		}
		_, rule := hostPort(strings.ToLower(id.Host))
		rules = append(rules, rule)
	}
	for _, h := range egress {
		rule, err := egressRule(h)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	slices.Sort(rules)
	return slices.Compact(rules), nil
}

// applyRules brings the sandbox's network rules from st.Rules to want:
// it allows what is new in one call and removes what went.
func (o instanceOps) applyRules(ctx context.Context, st *FrideInstanceState, want []string) error {
	var add, gone []string
	for _, r := range want {
		if !slices.Contains(st.Rules, r) {
			add = append(add, r)
		}
	}
	for _, r := range st.Rules {
		if !slices.Contains(want, r) {
			gone = append(gone, r)
		}
	}
	if len(add) > 0 {
		if _, stderr, err := o.d.run(ctx, nil, nil, "policy", "allow", "network", "--sandbox", st.Sandbox, strings.Join(add, ",")); err != nil {
			return fmt.Errorf("sbx could not allow %s: %w", strings.Join(add, ", "), sbxError(err, stderr))
		}
	}
	for _, r := range gone {
		if _, stderr, err := o.d.run(ctx, nil, nil, "policy", "rm", "network", "--sandbox", st.Sandbox, "--resource", r, "--force"); err != nil {
			return fmt.Errorf("sbx could not remove the rule for %s: %w", r, sbxError(err, stderr))
		}
	}
	if len(add) > 0 || len(gone) > 0 {
		o.say("%s may reach %s.", st.Name, strings.Join(want, ", "))
	}
	st.Rules = slices.Clone(want)
	return nil
}

// codexLogin reports whether sbx holds the ChatGPT login Codex runs
// with: then the instances may reach OpenAI.
func (o instanceOps) codexLogin(ctx context.Context) bool {
	out, _, err := o.d.run(ctx, nil, nil, "secret", "ls", "-g", "--service", "openai", "--json")
	return err == nil && sbxCodexLogin(out)
}
