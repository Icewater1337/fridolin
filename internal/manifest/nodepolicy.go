package manifest

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// NodeRule is brain.json's node_policy entry for one folder and
// everything inside it (docs/FILES.md, "Node policy"): where copies of it
// may land, and which nodes may run which kinds of work on it. A rule
// that leaves a dimension out does not limit it; nothing limits a folder
// no rule covers.
type NodeRule struct {
	// Folder is relative to the brain folder, with / between names ("" is
	// the whole brain folder), or an absolute path: a folder on a node
	// (or on the core), in its own spelling, or starting with ~/ for the
	// core's home.
	Folder string `json:"folder"`
	// Storage is where copies of the folder may land: nodes by name or
	// id, backup targets by name, and "cloud" (the vault's B2 tier and
	// snapshots, the Drive backup, a core export). nil: anywhere; []:
	// nowhere off its machine.
	Storage *[]string `json:"storage,omitempty"`
	// Tasks are, by kind (NodeTaskKinds), the nodes that may run that
	// kind of work on the folder; a kind left out runs anywhere, one with
	// [] nowhere.
	Tasks map[string][]string `json:"tasks,omitempty"`
	// Repos are FrIDE repositories, by their folder name, that belong to
	// this folder: work in them gets the folder's coding nodes.
	Repos []string `json:"repos,omitempty"`
}

// The kinds of work a NodeRule names nodes for.
const (
	// TaskBackup is a backup of the folder: a node target, or "cloud".
	TaskBackup = "backup"
	// TaskCoding is a FrIDE task or instance, or a node agent session.
	TaskCoding = "coding"
	// TaskExec is node_exec with its dir in the folder, or a terminal.
	TaskExec = "exec"
)

// NodeTaskKinds are the kinds node_policy's tasks may name.
func NodeTaskKinds() []string { return []string{TaskBackup, TaskCoding, TaskExec} }

// Bounds of node_policy.
const (
	maxNodeRules     = 64
	maxNodeRuleList  = 64
	maxNodeRuleEntry = 128
)

var policyRepoRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// IsAbsFolder reports whether folder is absolute in Unix or Windows
// spelling, or starts with ~/.
func IsAbsFolder(folder string) bool {
	if strings.HasPrefix(folder, "/") || strings.HasPrefix(folder, "~/") || strings.HasPrefix(folder, `\\`) {
		return true
	}
	return len(folder) >= 3 && unicode.IsLetter(rune(folder[0])) && folder[1] == ':' && (folder[2] == '\\' || folder[2] == '/')
}

// ValidPolicyFolder reports whether folder may be a NodeRule's folder:
// "", a relative path of plain names, or an absolute path without "..".
func ValidPolicyFolder(folder string) bool {
	if folder == "" {
		return true
	}
	if len(folder) > 4096 || strings.ContainsAny(folder, "\x00\r\n") || !utf8.ValidString(folder) {
		return false
	}
	if IsAbsFolder(folder) {
		return !slices.Contains(strings.FieldsFunc(folder, func(r rune) bool { return r == '/' || r == '\\' }), "..")
	}
	return ValidUploadFolder(folder)
}

func validPolicyEntry(e string) bool {
	return strings.TrimSpace(e) == e && e != "" && len(e) <= maxNodeRuleEntry && !strings.ContainsFunc(e, unicode.IsControl)
}

// validateNodePolicy reports what is wrong with node_policy.
func validateNodePolicy(rules []NodeRule) error {
	if len(rules) > maxNodeRules {
		return fmt.Errorf("node_policy: at most %d rules", maxNodeRules)
	}
	seen := map[string]bool{}
	list := func(folder, what string, l []string) error {
		if len(l) > maxNodeRuleList {
			return fmt.Errorf("node_policy %q: %s lists more than %d", folder, what, maxNodeRuleList)
		}
		for _, e := range l {
			if !validPolicyEntry(e) {
				return fmt.Errorf("node_policy %q: %s: %q is not a node, target or \"cloud\"", folder, what, e)
			}
		}
		return nil
	}
	for _, r := range rules {
		if !ValidPolicyFolder(r.Folder) {
			return fmt.Errorf("node_policy: folder %q must be a path inside the brain folder (\"\" for all of it) or an absolute path", r.Folder)
		}
		if seen[r.Folder] {
			return fmt.Errorf("node_policy lists %q twice", r.Folder)
		}
		seen[r.Folder] = true
		if r.Storage != nil {
			if err := list(r.Folder, "storage", *r.Storage); err != nil {
				return err
			}
		}
		for kind, l := range r.Tasks {
			if !slices.Contains(NodeTaskKinds(), kind) {
				return fmt.Errorf("node_policy %q: tasks.%s is none of %s", r.Folder, kind, strings.Join(NodeTaskKinds(), ", "))
			}
			if err := list(r.Folder, "tasks."+kind, l); err != nil {
				return err
			}
		}
		if len(r.Repos) > maxNodeRuleList {
			return fmt.Errorf("node_policy %q: repos lists more than %d", r.Folder, maxNodeRuleList)
		}
		for _, name := range r.Repos {
			if !policyRepoRe.MatchString(name) {
				return fmt.Errorf("node_policy %q: repos: %q is not a repository's folder name", r.Folder, name)
			}
		}
	}
	return nil
}

// FolderRule is a NodeRule with its folder made absolute, and the brain
// whose brain.json holds it.
type FolderRule struct {
	Brain string
	Path  string
	Rule  NodeRule
}

// FolderRules are m's node_policy rules with their folders made absolute:
// a relative one inside dir, ~/ in home.
func (m Manifest) FolderRules(dir, home string) []FolderRule {
	out := make([]FolderRule, 0, len(m.NodePolicy))
	for _, r := range m.NodePolicy {
		p := r.Folder
		switch {
		case strings.HasPrefix(p, "~/"):
			p = home + p[1:]
		case !IsAbsFolder(p):
			p = strings.TrimSuffix(dir, "/") + "/" + p
		}
		out = append(out, FolderRule{Brain: m.ID, Path: policyPath(p), Rule: r})
	}
	return out
}

// PolicyPath is p as rules compare paths (policyPath).
func PolicyPath(p string) string { return policyPath(p) }

// policyPath is p as rules compare paths: / between names, no trailing
// one, a Windows drive letter in lower case.
func policyPath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	unc := strings.HasPrefix(p, "//")
	p = path.Clean(p)
	if unc {
		p = "/" + p
	}
	if len(p) >= 2 && p[1] == ':' {
		p = strings.ToLower(p[:1]) + p[1:]
	}
	return p
}

// within reports whether p (policyPath'd) is folder or inside it.
func within(p, folder string) bool {
	return p == folder || folder == "/" || strings.HasPrefix(p, folder+"/")
}

// Limit is what node_policy allows for one dimension of one folder.
type Limit struct {
	// Set says a rule limits it; Folder is that rule's folder, Brain its
	// brain.
	Set    bool
	Folder string
	Brain  string
	// Nodes are what it allows: names or ids of nodes, names of backup
	// targets, "cloud".
	Nodes []string
}

// Allows reports whether l lets any of keys (a node's id and name, a
// target's name, "cloud") through; one with no rule lets all through.
func (l Limit) Allows(keys ...string) bool {
	if !l.Set {
		return true
	}
	return slices.ContainsFunc(l.Nodes, func(n string) bool {
		return slices.ContainsFunc(keys, func(k string) bool { return k != "" && strings.EqualFold(k, n) })
	})
}

// Text says what l allows, for a log line or an error: "only icebase,
// mac-mini (node_policy of /x/work)".
func (l Limit) Text() string {
	if !l.Set {
		return "anywhere"
	}
	what := "nowhere"
	if len(l.Nodes) > 0 {
		what = "only on " + strings.Join(l.Nodes, ", ")
	}
	return fmt.Sprintf("%s (node_policy of %s, brain %s)", what, l.Folder, l.Brain)
}

// Policy is every folder's rule, from every brain.
type Policy []FolderRule

// closest is the limit of the rule closest to p that sets what get
// reads; get answers nil, false for a rule that does not set it.
func (p Policy) closest(at string, get func(NodeRule) ([]string, bool)) Limit {
	at = policyPath(at)
	var best Limit
	for _, r := range p {
		l, ok := get(r.Rule)
		switch {
		case !ok || !within(at, r.Path) || best.Set && len(r.Path) < len(best.Folder):
		case best.Set && r.Path == best.Folder:
			// Two brains' rules for one folder: what both allow.
			best.Nodes = slices.DeleteFunc(slices.Clone(best.Nodes), func(n string) bool { return !(Limit{Set: true, Nodes: l}).Allows(n) })
			best.Brain += ", " + r.Brain
		default:
			best = Limit{Set: true, Folder: r.Path, Brain: r.Brain, Nodes: l}
		}
	}
	return best
}

func storageOf(r NodeRule) ([]string, bool) {
	if r.Storage == nil {
		return nil, false
	}
	return *r.Storage, true
}

func taskOf(kind string) func(NodeRule) ([]string, bool) {
	return func(r NodeRule) ([]string, bool) {
		l, ok := r.Tasks[kind]
		return l, ok
	}
}

// Storage is where copies of the folder at may land.
func (p Policy) Storage(at string) Limit { return p.closest(at, storageOf) }

// Task is which nodes may run work of kind on the folder at.
func (p Policy) Task(kind, at string) Limit { return p.closest(at, taskOf(kind)) }

// RepoTask are the limits on work of kind in the FrIDE repositories
// repos: those of every rule that lists one of them and sets kind.
func (p Policy) RepoTask(kind string, repos []string) []Limit {
	var out []Limit
	for _, r := range p {
		l, ok := r.Rule.Tasks[kind]
		if ok && slices.ContainsFunc(r.Rule.Repos, func(n string) bool { return slices.Contains(repos, n) }) {
			out = append(out, Limit{Set: true, Folder: r.Path, Brain: r.Brain, Nodes: l})
		}
	}
	return out
}

// Backup are the limits on a backup of the folder at: storage and
// tasks.backup, both of which must let a target through.
func (p Policy) Backup(at string) []Limit {
	return []Limit{p.Storage(at), p.Task(TaskBackup, at)}
}

// Denying is the first of limits that does not let keys through, and
// whether there is one.
func Denying(limits []Limit, keys ...string) (Limit, bool) {
	for _, l := range limits {
		if !l.Allows(keys...) {
			return l, true
		}
	}
	return Limit{}, false
}

// BackupDenied says what of the folder dir a backup to the target keys
// name may not take: all of it (why says by which rule), or the folders
// below it, as patterns of backup.exclude's syntax ("/work/"). A rule
// below one that keeps a folder out cannot let a part of it back in: it
// stays out.
func (p Policy) BackupDenied(dir string, keys ...string) (all bool, why string, patterns []string) {
	dir = policyPath(dir)
	if l, deny := Denying(p.Backup(dir), keys...); deny {
		return true, l.Text(), nil
	}
	for _, r := range p {
		rel, ok := strings.CutPrefix(r.Path, dir+"/")
		if !ok {
			continue
		}
		if _, deny := Denying(p.Backup(r.Path), keys...); deny {
			patterns = append(patterns, "/"+rel+"/")
		}
	}
	slices.Sort(patterns)
	patterns = slices.Compact(patterns)
	// One inside another that stays out says nothing more.
	var out []string
	for _, x := range patterns {
		if !slices.ContainsFunc(patterns, func(y string) bool { return x != y && strings.HasPrefix(x, y) }) {
			out = append(out, x)
		}
	}
	return false, "", out
}
