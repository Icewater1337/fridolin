package nodeagent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The fake sbx's sandbox-scoped rules, github secrets and mounts, as sbx
// v0.50.0 keeps them: rules.json lists the rules (one per host, ids
// rule-1, rule-2, …), github-<sandbox> holds a sandbox's github secret,
// mounts-<sandbox> its `sbx mount`s, one per line. Removing a sandbox
// removes all three.

// fakeRule is a rule as `sbx policy ls --json` prints it.
type fakeRule struct {
	ID        string   `json:"id"`
	Scope     string   `json:"scope"`
	Type      string   `json:"resource_type"`
	Decision  string   `json:"decision"`
	Resources []string `json:"resources"`
	Status    string   `json:"status"`
}

func fakeRules(dir string) []fakeRule {
	var rules []fakeRule
	b, _ := os.ReadFile(filepath.Join(dir, "rules.json"))
	_ = json.Unmarshal(b, &rules)
	return rules
}

func saveFakeRules(dir string, rules []fakeRule) {
	b, _ := json.Marshal(rules)
	_ = os.WriteFile(filepath.Join(dir, "rules.json"), b, 0o600)
}

// fakeRuleAdd is `sbx policy allow|deny network --sandbox NAME HOSTS`: a
// rule per host the sandbox has none for yet.
func fakeRuleAdd(dir, decision string, args []string) int {
	i := slices.Index(args, "--sandbox")
	if i < 0 || len(args) < i+3 {
		fmt.Fprintf(os.Stderr, "policy %s: %v\n", decision, args)
		return 2
	}
	scope := "sandbox:" + args[i+1]
	rules := fakeRules(dir)
	next, _ := strconv.Atoi(cannedOr(dir, "rule-next", "1"))
	for _, h := range strings.Split(args[len(args)-1], ",") {
		if j := slices.IndexFunc(rules, func(r fakeRule) bool { return r.Scope == scope && slices.Contains(r.Resources, h) }); j >= 0 {
			fmt.Printf("Already covered in policy local (scope: %s): %s [tcp] (by rule %q)\n", scope, h, rules[j].ID)
			continue
		}
		id := fmt.Sprintf("rule-%d", next)
		next++
		rules = append(rules, fakeRule{ID: id, Scope: scope, Type: "network", Decision: decision, Resources: []string{h}, Status: "active"})
		fmt.Printf("Added rule %s to policy local (scope: %s): %s [tcp]\n", id, scope, h)
	}
	saveFakeRules(dir, rules)
	_ = os.WriteFile(filepath.Join(dir, "rule-next"), []byte(strconv.Itoa(next)), 0o600)
	return 0
}

// fakeRuleRm is `sbx policy rm network --sandbox NAME --id ID --force`.
func fakeRuleRm(dir string, args []string) int {
	i := slices.Index(args, "--id")
	if i < 0 || !slices.Contains(args, "--force") {
		fmt.Fprintln(os.Stderr, "Error: stdin is not a terminal; pass --force to skip the confirmation")
		return 1
	}
	rules := fakeRules(dir)
	n := len(rules)
	rules = slices.DeleteFunc(rules, func(r fakeRule) bool { return r.ID == args[i+1] })
	if len(rules) == n {
		fmt.Fprintf(os.Stderr, "Error: no rule %s\n", args[i+1])
		return 1
	}
	saveFakeRules(dir, rules)
	fmt.Printf("Removed rule from policy local: id=%s\n", args[i+1])
	return 0
}

// fakePolicyJSON is the global policy's JSON with the sandboxes' rules.
func fakePolicyJSON(dir, global string) string {
	var p struct {
		Rules []json.RawMessage `json:"rules"`
	}
	if json.Unmarshal([]byte(global), &p) != nil {
		return global
	}
	for _, r := range fakeRules(dir) {
		b, _ := json.Marshal(r)
		p.Rules = append(p.Rules, b)
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// fakeGitHubSecret is `sbx secret set github --sandbox NAME`, the value
// on stdin, and `sbx secret rm github --sandbox NAME -f`.
func fakeGitHubSecret(dir string, args []string) int {
	i := slices.Index(args, "--sandbox")
	if i < 0 {
		return 2
	}
	file := filepath.Join(dir, "github-"+args[i+1])
	if args[0] == "rm" {
		if err := os.Remove(file); err != nil {
			fmt.Fprintln(os.Stderr, "Error: no github secret")
			return 1
		}
		fmt.Println("Removed secret tok-ab…wxyz") // part of a value: never logged
		return 0
	}
	fmt.Fprint(os.Stderr, "Enter secret: ")
	b, _ := io.ReadAll(os.Stdin)
	if _, err := os.Stat(filepath.Join(dir, "fail-secret-github")); err == nil {
		// An error that shows the value, which the agent must not pass on.
		fmt.Fprintf(os.Stderr, "Error: %s is not a token\n", b)
		return 1
	}
	_ = os.WriteFile(file, b, 0o600)
	fmt.Println("Secret stored")
	return 0
}

// fakeMount is `sbx mount NAME HOST:TARGET`, recorded in mounts-NAME.
func fakeMount(dir string, args []string) int {
	if len(args) != 2 || !slices.Contains(fakeSandboxes(dir), args[0]) {
		fmt.Fprintln(os.Stderr, "error: mount: no such sandbox")
		return 1
	}
	if _, err := os.Stat(filepath.Join(dir, "stopped-"+args[0])); err == nil {
		_ = os.Remove(filepath.Join(dir, "stopped-"+args[0]))
		fmt.Fprintf(os.Stderr, "error: mount: add allowed path: request failed: 409 Conflict: sandbox %q is not running\n", args[0])
		return 1
	}
	appendFile(filepath.Join(dir, "mounts-"+args[0]), args[1]+"\n")
	fmt.Printf("Mounted %s in sandbox %s\n", args[1], args[0])
	return 0
}

// fakeGitHubSeen is what a run in sandbox name has of GitHub: "secret"
// when the sandbox has a github secret, whose value it keeps in
// github-seen for the test (sbx's proxy has it, the VM never), and each
// GitHub host its rules allow.
func fakeGitHubSeen(dir, name string) string {
	var seen []string
	if b, err := os.ReadFile(filepath.Join(dir, "github-"+name)); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "github-seen"), b, 0o600)
		seen = append(seen, "secret")
	}
	for _, r := range fakeRules(dir) {
		if r.Scope == "sandbox:"+name && r.Decision == "allow" && strings.Contains(r.Resources[0], "github") {
			seen = append(seen, r.Resources[0])
		}
	}
	return strings.Join(seen, " ")
}

// fakeForget is what `sbx rm NAME` removes besides the sandbox: its
// rules, its github secret and its mounts.
func fakeForget(dir, name string) {
	saveFakeRules(dir, slices.DeleteFunc(fakeRules(dir), func(r fakeRule) bool { return r.Scope == "sandbox:"+name }))
	_ = os.Remove(filepath.Join(dir, "github-"+name))
	_ = os.Remove(filepath.Join(dir, "mounts-"+name))
}
