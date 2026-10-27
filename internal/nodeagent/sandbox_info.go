package nodeagent

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

// Parsers of sbx's output. sbx is young (v0.50.0 when fridolin started
// to use it) and its formats change, so each reads what it can and
// says "no" when it cannot.

var sbxVersionRe = regexp.MustCompile(`v?[0-9]+\.[0-9]+\.[0-9]+[0-9A-Za-z.+-]*`)

// parseSbxVersion is the version `sbx version` prints, "" for none.
func parseSbxVersion(out []byte) string {
	v := sbxVersionRe.Find(out)
	if len(v) > 64 {
		v = v[:64]
	}
	return string(v)
}

// sbxSandbox is one row of `sbx ls --json`.
type sbxSandbox struct {
	Name, Status, Agent string
}

// sbxRows is the rows of a JSON listing: the array itself (sbx 0.48 and
// later), or the first array in a wrapper object ({"sandboxes": […]}
// before).
func sbxRows(out []byte) ([]map[string]any, bool) {
	var rows []map[string]any
	if json.Unmarshal(out, &rows) == nil {
		return rows, true
	}
	var wrap map[string]json.RawMessage
	if json.Unmarshal(out, &wrap) != nil {
		return nil, false
	}
	for _, raw := range wrap {
		if json.Unmarshal(raw, &rows) == nil {
			return rows, true
		}
	}
	return nil, false
}

// str is the first of keys that row holds as a string.
func str(row map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := row[k].(string); ok {
			return s
		}
	}
	return ""
}

// parseSbxList reads `sbx ls --json`.
func parseSbxList(out []byte) ([]sbxSandbox, bool) {
	rows, ok := sbxRows(out)
	if !ok {
		return nil, false
	}
	list := make([]sbxSandbox, 0, len(rows))
	for _, r := range rows {
		list = append(list, sbxSandbox{Name: str(r, "name", "Name"), Status: str(r, "status", "state", "Status"), Agent: str(r, "agent", "Agent")})
	}
	return list, true
}

// sbxListsTemplate reports whether `sbx template ls --json` lists the
// template tpl: rows of "repository" (docker.io/library/NAME for an
// image loaded from Docker) and "tag".
func sbxListsTemplate(out []byte, tpl string) bool {
	rows, _ := sbxRows(out)
	for _, r := range rows {
		ref := str(r, "repository") + ":" + str(r, "tag")
		if ref == tpl || strings.TrimPrefix(ref, "docker.io/library/") == tpl || strings.TrimPrefix(ref, "docker.io/") == tpl {
			return true
		}
	}
	return false
}

// sbxSaysSignIn reports whether an error of sbx is about Docker's
// sign-in, rather than the daemon.
func sbxSaysSignIn(msg string) bool {
	m := strings.ToLower(msg)
	for _, w := range []string{"sign in", "signed in", "sign-in", "log in", "logged in", "login", "authenticat", "unauthorized"} {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

// sbxCodexLogin reports whether `sbx secret ls -g --service openai
// --json` lists the global openai secret: sbx's ChatGPT login, which
// Codex runs use. sbx v0.50.0 prints {"secrets": [{"name", "scope",
// "secret", "type"}], "custom_secrets": [...], ...}; -g leaves out the
// sandboxes' own. It reads names only; the listing is never logged,
// since its "secret" holds part of the value.
func sbxCodexLogin(out []byte) bool {
	var l struct {
		Secrets []map[string]any `json:"secrets"`
	}
	if json.Unmarshal(out, &l) != nil {
		return false
	}
	return slices.ContainsFunc(l.Secrets, func(r map[string]any) bool {
		return strings.EqualFold(str(r, "name", "service"), "openai")
	})
}

// sbxDiagnosis is what `sbx diagnose --json` says of the daemon and of
// Docker's sign-in: its checks "Daemon" and "Authentication".
type sbxDiagnosis struct {
	Daemon, SignedIn bool
}

// parseSbxDiagnose reads `sbx diagnose --json`; false when it lacks
// either check.
func parseSbxDiagnose(out []byte) (sbxDiagnosis, bool) {
	var d struct {
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	var r sbxDiagnosis
	if json.Unmarshal(out, &d) != nil {
		return r, false
	}
	seen := 0
	for _, c := range d.Checks {
		switch c.Name {
		case "Daemon":
			r.Daemon, seen = c.Status == "pass", seen+1
		case "Authentication":
			r.SignedIn, seen = c.Status == "pass", seen+1
		}
	}
	return r, seen == 2
}

// The global settings fridolin owns on the node's machine, with the
// value each must have.
var sbxOwnedSettings = map[string]string{
	"ssh.agentForwardingEnabled": "false",
	"browser.enabled":            "false",
}

// sbxSetting is the value of key in `sbx settings ls`, "" when it is not
// there: a JSON object (flat or nested by the key's parts), JSON rows of
// key and value, or lines of text "key value", "key = value" or
// "key: value".
func sbxSetting(out []byte, key string) string {
	var obj map[string]any
	if json.Unmarshal(out, &obj) == nil {
		var cur any = obj
		if v, ok := obj[key]; ok {
			cur = v
		} else {
			for _, part := range strings.Split(key, ".") {
				m, ok := cur.(map[string]any)
				if !ok {
					return ""
				}
				cur = m[part]
			}
		}
		return scalar(cur)
	}
	if rows, ok := sbxRows(out); ok {
		for _, r := range rows {
			if str(r, "key", "name", "setting") == key {
				return scalar(r["value"])
			}
		}
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' || r == '=' || r == ':' || r == '|' })
		if len(f) >= 2 && f[0] == key {
			return f[1]
		}
	}
	return ""
}

func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	return ""
}

// sbxSettingsDrift is the owned settings that do not have their value
// in `sbx settings ls --json`, sorted.
func sbxSettingsDrift(settings []byte) []string {
	var out []string
	for k, want := range sbxOwnedSettings {
		if !strings.EqualFold(sbxSetting(settings, k), want) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// sbxDenyAll reports whether `sbx policy ls --json` holds the global
// network policy deny-all, as `sbx policy init deny-all` makes it: an
// active global deny of every host ("**"), and no active global network
// allow. The kits' and fridolin's own rules are sandbox-scoped.
func sbxDenyAll(policy []byte) bool {
	var p struct {
		Rules []struct {
			Scope     string   `json:"scope"`
			Type      string   `json:"resource_type"`
			Decision  string   `json:"decision"`
			Status    string   `json:"status"`
			Resources []string `json:"resources"`
		} `json:"rules"`
	}
	if json.Unmarshal(policy, &p) != nil {
		return false
	}
	deny := false
	for _, r := range p.Rules {
		if r.Scope != "global" || r.Type != "network" || r.Status != "active" {
			continue
		}
		switch r.Decision {
		case "allow":
			return false
		case "deny":
			deny = deny || slices.Contains(r.Resources, "**")
		}
	}
	return deny
}

// sbxSettingsHold reports whether fridolin's global settings hold: the
// owned settings have their values, and the global network policy is
// deny-all.
func sbxSettingsHold(settings, policy []byte) bool {
	return len(sbxSettingsDrift(settings)) == 0 && sbxDenyAll(policy)
}
