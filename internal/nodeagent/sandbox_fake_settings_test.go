package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The fake sbx's settings, policy and templates, in the forms sbx
// v0.50.0 prints them.

// fakeSettingsValues are the owned settings in the fake: settings.json,
// else fridolin's values (a node that was set up).
func fakeSettingsValues(dir string) map[string]bool {
	m := map[string]bool{"browser.enabled": false, "ssh.agentForwardingEnabled": false}
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// fakeSettings is `sbx settings ls --json | set KEY VALUE`.
func fakeSettings(dir string, args []string) int {
	m := fakeSettingsValues(dir)
	switch {
	case len(args) == 2 && args[0] == "ls" && args[1] == "--json":
		rows := []map[string]any{{"key": "claude.remoteControl", "value": false, "type": "bool"}}
		for k, v := range m {
			rows = append(rows, map[string]any{"key": k, "value": v, "type": "bool", "source": "override"})
		}
		b, _ := json.Marshal(rows)
		fmt.Println(string(b))
	case len(args) == 3 && args[0] == "set":
		m[args[1]] = args[2] == "true"
		b, _ := json.Marshal(m)
		_ = os.WriteFile(filepath.Join(dir, "settings.json"), b, 0o600)
		fmt.Printf("Updated setting %s\n", args[1])
	default:
		fmt.Fprintf(os.Stderr, "settings: %v\n", args)
		return 2
	}
	return 0
}

// fakeDenyAll is `sbx policy ls --json` after `sbx policy init deny-all`.
const fakeDenyAll = `{"rules":[{"id":"default-fs-read-allow-all","scope":"global","resource_type":"filesystem:read","decision":"allow","resources":["**"],"status":"active"},` +
	`{"id":"default-deny-all","scope":"global","resource_type":"network","decision":"deny","resources":["**"],"status":"active"}]}`

// fakeBalanced is a global policy that allows hosts.
const fakeBalanced = `{"rules":[{"id":"ai","scope":"global","resource_type":"network","decision":"allow","resources":["api.anthropic.com:443"],"status":"active"}]}`

// fakePolicy is `sbx policy ls --json | init deny-all | reset --force |
// allow|deny network --sandbox NAME HOST`. The file policy-unset is a
// policy never initialized; policy holds another.
func fakePolicy(dir string, args []string) int {
	unset := filepath.Join(dir, "policy-unset")
	switch args[0] {
	case "ls":
		if _, err := os.Stat(unset); err == nil {
			fmt.Fprintln(os.Stderr, "error: global network policy has not been initialized\n  try: sbx policy init <allow-all|balanced|deny-all>")
			return 1
		}
		fmt.Println(cannedOr(dir, "policy", fakeDenyAll))
	case "init":
		if _, err := os.Stat(unset); err != nil {
			fmt.Fprintln(os.Stderr, "error: the global network policy is initialized already")
			return 1
		}
		_ = os.Remove(unset)
		_ = os.WriteFile(filepath.Join(dir, "policy"), []byte(fakeDenyAll), 0o600)
	case "reset":
		_ = os.WriteFile(unset, nil, 0o600)
	}
	return 0
}

// fakeTemplate is `sbx template ls --json | load FILE`: templates.json
// lists the refs (default fridolin-brain:v1); load adds the ref the file
// holds (the fake docker writes it there).
func fakeTemplate(dir string, args []string) int {
	refs := []string{"fridolin-brain:v1"}
	if b, err := os.ReadFile(filepath.Join(dir, "templates.json")); err == nil {
		_ = json.Unmarshal(b, &refs)
	}
	switch args[0] {
	case "ls":
		rows := []map[string]string{{"repository": "docker.io/docker/sandbox-templates", "tag": "claude-code-docker"}}
		for _, r := range refs {
			name, tag, _ := strings.Cut(r, ":")
			rows = append(rows, map[string]string{"repository": "docker.io/library/" + name, "tag": tag})
		}
		b, _ := json.Marshal(rows)
		fmt.Println(string(b))
	case "load":
		b, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		b, _ = json.Marshal(append(refs, strings.TrimSpace(string(b))))
		_ = os.WriteFile(filepath.Join(dir, "templates.json"), b, 0o600)
		fmt.Println("Loaded image")
	}
	return 0
}
