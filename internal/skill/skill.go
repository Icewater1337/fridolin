// Package skill reads the skills of a brain: markdown files under its
// skills/ folder, either skills/<name>.md or skills/<name>/SKILL.md, with
// a frontmatter in the portable Agent Skills format (name, description,
// requires.env, requires.bins, os, install) and Fridolin's own fields
// under metadata.fridolin (risk, vault, approve, triggers). It parses and
// validates; reading the files, and which policy is approved, is the
// bridge's. Brains write these files, so nothing here trusts them.
package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Risk is how far a skill's steps reach, the highest of them: read
// (search, summarise, show), write (files in the vault, a calendar
// event; undone by a revert) or irreversible (submit, send to a third
// party, pay, delete, publish), which stops for the owner's yes.
type Risk string

const (
	RiskRead         Risk = "read"
	RiskWrite        Risk = "write"
	RiskIrreversible Risk = "irreversible"
)

// Risks are the levels, lowest first.
var Risks = []Risk{RiskRead, RiskWrite, RiskIrreversible}

// Rank orders the levels: read 0, write 1, irreversible 2, anything else
// -1.
func (r Risk) Rank() int { return slices.Index(Risks, r) }

// Valid reports whether r is one of the levels.
func (r Risk) Valid() bool { return r.Rank() >= 0 }

// Max is the higher of two levels; an invalid one counts as read.
func Max(a, b Risk) Risk {
	if b.Rank() > a.Rank() {
		return b
	}
	if !a.Valid() {
		return RiskRead
	}
	return a
}

// ParseRisk reads a level; "" is read.
func ParseRisk(s string) (Risk, error) {
	if s == "" {
		return RiskRead, nil
	}
	r := Risk(strings.ToLower(strings.TrimSpace(s)))
	if !r.Valid() {
		return "", fmt.Errorf("%q is not read, write or irreversible", s)
	}
	return r, nil
}

// Policy is what a skill declares under metadata.fridolin.
type Policy struct {
	Risk Risk `json:"risk"`
	// Vault lists the only vault paths (globs) a run of the skill uses.
	Vault []string `json:"vault"`
	// Approve names the steps that stop for the owner's yes.
	Approve []string `json:"approve"`
	// Triggers are words or phrases that, in a message, make the bridge
	// count the run as one of this skill.
	Triggers []string `json:"triggers"`
}

// Normalized fills a policy's empty values: read, and empty lists.
func (p Policy) Normalized() Policy {
	if p.Risk == "" {
		p.Risk = RiskRead
	}
	for _, l := range []*[]string{&p.Vault, &p.Approve, &p.Triggers} {
		if *l == nil {
			*l = []string{}
		}
	}
	return p
}

// Hash identifies the policy, for an approval of it.
func (p Policy) Hash() string {
	data, _ := json.Marshal(p.Normalized())
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Requires is what a skill needs on the machine.
type Requires struct {
	Env  []string `json:"env"`
	Bins []string `json:"bins"`
}

// Skill is one skill file, read.
type Skill struct {
	// Path is the file, relative to the brain folder and slash-separated:
	// skills/<name>.md or skills/<name>/SKILL.md.
	Path        string   `json:"path"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Requires    Requires `json:"requires"`
	OS          []string `json:"os"`
	// Install are the steps that install what it requires, each a map of
	// strings (kind, formula, package, …), or {"run": "<command>"} for
	// a plain string.
	Install []map[string]string `json:"install"`
	Policy  Policy              `json:"fridolin"`
	// Frontmatter is false for a skill file without one, from before the
	// format: its name is the file's and its policy read.
	Frontmatter bool `json:"frontmatter"`
}

// MaxFrontmatter bounds a frontmatter.
const MaxFrontmatter = 64 << 10

// Split separates a file's frontmatter, between a first line --- and the
// next line --- (or ...), from its body. ok is false for a file without
// one; err says why one that starts is not closed.
func Split(data string) (front, body string, ok bool, err error) {
	data = strings.TrimPrefix(data, "\ufeff")
	first, rest, found := strings.Cut(data, "\n")
	if strings.TrimRight(first, " \r") != "---" {
		return "", data, false, nil
	}
	if !found {
		return "", "", true, errors.New("the frontmatter is not closed by a line ---")
	}
	off := 0
	for off <= len(rest) {
		line, _, more := strings.Cut(rest[off:], "\n")
		if t := strings.TrimRight(line, " \r"); t == "---" || t == "..." {
			front = rest[:off]
			if !more {
				return front, "", true, nil
			}
			return front, rest[off+len(line)+1:], true, nil
		}
		if !more {
			break
		}
		off += len(line) + 1
	}
	return "", "", true, errors.New("the frontmatter is not closed by a line ---")
}

// IsSkillPath reports whether rel, relative to a brain folder, is where
// a skill lives: skills/<name>.md (not README.md) or
// skills/<folder>/SKILL.md.
func IsSkillPath(rel string) bool {
	parts := strings.Split(rel, "/")
	switch {
	case len(parts) == 2 && parts[0] == "skills":
		return strings.HasSuffix(parts[1], ".md") && !strings.EqualFold(parts[1], "README.md") && len(parts[1]) > 3
	case len(parts) == 3 && parts[0] == "skills":
		return parts[1] != "" && parts[2] == "SKILL.md"
	}
	return false
}

// nameRe is a skill's name as the portable format has it.
var nameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

var envRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Limits on what a frontmatter declares.
const (
	maxName        = 64
	maxDescription = 1024
	maxListItems   = 50
	maxItem        = 256
	maxTrigger     = 100
	maxStep        = 64
)

// OSes are the values os takes; win32 is read as windows.
var OSes = []string{"darwin", "linux", "windows"}

// fridolinKeys are the fields under metadata.fridolin.
var fridolinKeys = []string{"risk", "vault", "approve", "triggers"}

// Parse reads the skill file rel (see IsSkillPath) with contents data. A
// file without a frontmatter is a skill all the same, named after the
// file, risk read. err lists every problem of a frontmatter; the Skill
// returned with it has what could be read.
func Parse(rel string, data []byte) (Skill, error) {
	s := Skill{Path: rel, Name: defaultName(rel), Policy: Policy{Risk: RiskRead}.Normalized(), OS: []string{}, Install: []map[string]string{}, Requires: Requires{Env: []string{}, Bins: []string{}}}
	front, body, ok, err := Split(string(data))
	if err != nil {
		return s, err
	}
	if !ok {
		s.Description = firstHeading(body)
		return s, nil
	}
	s.Frontmatter = true
	if len(front) > MaxFrontmatter {
		return s, fmt.Errorf("the frontmatter is larger than %d KiB", MaxFrontmatter>>10)
	}
	root, err := ParseYAML(front)
	if err != nil {
		return s, err
	}
	var errs []error
	bad := func(field, format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", field, fmt.Sprintf(format, a...)))
	}
	str := func(field string, n *Node) string {
		if n == nil {
			return ""
		}
		if n.Kind != ScalarNode {
			bad(field, "must be text")
			return ""
		}
		return n.Value
	}
	list := func(field string, n *Node, max int) []string {
		out := []string{}
		if n == nil {
			return out
		}
		switch n.Kind {
		case ScalarNode:
			if n.Value != "" {
				out = append(out, n.Value)
			}
			return out
		case MapNode:
			bad(field, "must be a list")
			return out
		case SeqNode:
			// Its items, below.
		}
		if len(n.Items) > maxListItems {
			bad(field, "has more than %d entries", maxListItems)
			return out
		}
		for i, it := range n.Items {
			if it.Kind != ScalarNode {
				bad(fmt.Sprintf("%s[%d]", field, i), "must be text")
				continue
			}
			v := strings.TrimSpace(it.Value)
			switch {
			case v == "":
				bad(fmt.Sprintf("%s[%d]", field, i), "is empty")
			case len(v) > max:
				bad(fmt.Sprintf("%s[%d]", field, i), "is longer than %d bytes", max)
			case slices.Contains(out, v):
			default:
				out = append(out, v)
			}
		}
		return out
	}

	s.Name = str("name", root.Get("name"))
	switch {
	case s.Name == "":
		bad("name", "is missing")
	case len(s.Name) > maxName || !nameRe.MatchString(s.Name):
		bad("name", "%q must be lower-case letters, digits and single hyphens, at most %d", truncate(s.Name, 80), maxName)
	case strings.HasSuffix(rel, "/SKILL.md") && s.Name != path.Base(path.Dir(rel)):
		bad("name", "%q must be the name of its folder, %q", s.Name, path.Base(path.Dir(rel)))
	}
	s.Description = strings.TrimSpace(str("description", root.Get("description")))
	switch {
	case s.Description == "":
		bad("description", "is missing: say what the skill does and when to use it")
	case len(s.Description) > maxDescription:
		bad("description", "is longer than %d bytes", maxDescription)
	}
	if req := root.Get("requires"); req != nil {
		if req.Kind != MapNode {
			bad("requires", "must be a mapping with env and bins")
		} else {
			s.Requires.Env = list("requires.env", req.Get("env"), maxItem)
			s.Requires.Bins = list("requires.bins", req.Get("bins"), maxItem)
			for _, k := range req.Keys {
				if k != "env" && k != "bins" {
					bad("requires."+k, "is not a field (env, bins)")
				}
			}
		}
	}
	for _, e := range s.Requires.Env {
		if !envRe.MatchString(e) {
			bad("requires.env", "%q is not a variable name", truncate(e, 40))
		}
	}
	for _, b := range s.Requires.Bins {
		if strings.ContainsAny(b, "/\\") {
			bad("requires.bins", "%q must be a program's name, not a path", truncate(b, 40))
		}
	}
	for _, o := range list("os", root.Get("os"), maxItem) {
		o = strings.ToLower(o)
		if o == "win32" {
			o = "windows"
		}
		if !slices.Contains(OSes, o) {
			bad("os", "%q is not darwin, linux or windows", truncate(o, 40))
			continue
		}
		if !slices.Contains(s.OS, o) {
			s.OS = append(s.OS, o)
		}
	}
	if inst := root.Get("install"); inst != nil {
		s.Install = install(inst, bad)
	}
	meta := root.Get("metadata")
	if meta != nil && meta.Kind != MapNode {
		bad("metadata", "must be a mapping")
	}
	if fr := meta.Get("fridolin"); fr != nil {
		if fr.Kind != MapNode {
			bad("metadata.fridolin", "must be a mapping with risk, vault, approve and triggers")
		} else {
			for _, k := range fr.Keys {
				if !slices.Contains(fridolinKeys, k) {
					bad("metadata.fridolin."+k, "is not a field (%s)", strings.Join(fridolinKeys, ", "))
				}
			}
			r, err := ParseRisk(str("metadata.fridolin.risk", fr.Get("risk")))
			if err != nil {
				bad("metadata.fridolin.risk", "%v", err)
				r = RiskRead
			}
			s.Policy = Policy{
				Risk:     r,
				Vault:    list("metadata.fridolin.vault", fr.Get("vault"), maxItem),
				Approve:  list("metadata.fridolin.approve", fr.Get("approve"), maxStep),
				Triggers: list("metadata.fridolin.triggers", fr.Get("triggers"), maxTrigger),
			}.Normalized()
			for _, v := range s.Policy.Vault {
				if err := checkVaultPath(v); err != nil {
					bad("metadata.fridolin.vault", "%q %v", truncate(v, 60), err)
				}
			}
			for _, t := range s.Policy.Triggers {
				if len([]rune(t)) < 2 {
					bad("metadata.fridolin.triggers", "%q is too short to match only what it means", t)
				}
			}
		}
	}
	return s, errors.Join(errs...)
}

// install reads the install steps: strings or mappings of strings.
func install(n *Node, bad func(field, format string, a ...any)) []map[string]string {
	out := []map[string]string{}
	items := n.Items
	switch n.Kind {
	case ScalarNode:
		items = []*Node{n}
	case MapNode:
		items = []*Node{n}
	case SeqNode:
		// Its items are the steps.
	}
	if len(items) > maxListItems {
		bad("install", "has more than %d steps", maxListItems)
		return out
	}
	for i, it := range items {
		field := fmt.Sprintf("install[%d]", i)
		switch it.Kind {
		case ScalarNode:
			if strings.TrimSpace(it.Value) != "" {
				out = append(out, map[string]string{"run": it.Value})
			}
		case MapNode:
			step := map[string]string{}
			for _, k := range it.Keys {
				v := it.Map[k]
				switch v.Kind {
				case ScalarNode:
					step[k] = v.Value
				case SeqNode:
					parts := []string{}
					for _, x := range v.Items {
						if x.Kind == ScalarNode {
							parts = append(parts, x.Value)
						}
					}
					step[k] = strings.Join(parts, ", ")
				case MapNode:
					bad(field+"."+k, "must be text or a list")
				}
			}
			out = append(out, step)
		case SeqNode:
			bad(field, "must be a command or a mapping")
		}
	}
	return out
}

// checkVaultPath accepts a path or glob relative to the vault.
func checkVaultPath(p string) error {
	switch {
	case strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || strings.Contains(p, "\\"):
		return errors.New("must be relative to the vault")
	case slices.Contains(strings.Split(p, "/"), ".."):
		return errors.New("must not leave the vault (..)")
	}
	return nil
}

// defaultName is the name of a skill without a frontmatter: its file's,
// or its folder's.
func defaultName(rel string) string {
	if strings.HasSuffix(rel, "/SKILL.md") {
		return path.Base(path.Dir(rel))
	}
	return strings.TrimSuffix(path.Base(rel), ".md")
}

// slug makes name a valid skill name: lower case, a hyphen for each run
// of anything else.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
			continue
		}
		dash = true
	}
	if b.Len() == 0 {
		return "skill"
	}
	return truncate(b.String(), maxName)
}

// firstHeading is the text of a markdown file's first heading, without
// a "Skill:" in front.
func firstHeading(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "#") {
			t = strings.TrimSpace(strings.TrimLeft(t, "#"))
			t = strings.TrimSpace(strings.TrimPrefix(t, "Skill:"))
			return truncate(t, maxDescription)
		}
	}
	return ""
}

// Matches reports whether text, a message, names one of the skill's
// triggers: as a case-insensitive substring.
func (s Skill) Matches(text string) bool {
	if len(s.Policy.Triggers) == 0 || text == "" {
		return false
	}
	lower := strings.ToLower(text)
	for _, t := range s.Policy.Triggers {
		if strings.Contains(lower, strings.ToLower(t)) {
			return true
		}
	}
	return false
}

// WithPolicy returns data, a skill file, with its metadata.fridolin set to
// p: the frontmatter written anew (comments in it go), the body as it
// was. A file without a frontmatter gets one, named after the file, with
// description as its description.
func WithPolicy(rel string, data []byte, p Policy, description string) ([]byte, error) {
	front, body, ok, err := Split(string(data))
	if err != nil {
		return nil, err
	}
	root := NewMap()
	if ok {
		if root, err = ParseYAML(front); err != nil {
			return nil, err
		}
	}
	if root.Get("name") == nil {
		root.Set("name", NewScalar(slug(defaultName(rel))))
	}
	if root.Get("description") == nil {
		if description == "" {
			description = firstHeading(body)
		}
		if description == "" {
			description = defaultName(rel)
		}
		root.Set("description", NewScalar(description))
	}
	meta := root.Get("metadata")
	if meta == nil || meta.Kind != MapNode {
		meta = NewMap()
		root.Set("metadata", meta)
	}
	p = p.Normalized()
	fr := NewMap()
	fr.Set("risk", NewScalar(string(p.Risk)))
	fr.Set("vault", NewList(p.Vault))
	fr.Set("approve", NewList(p.Approve))
	fr.Set("triggers", NewList(p.Triggers))
	meta.Set("fridolin", fr)
	out := "---\n" + FormatYAML(root) + "---\n" + body
	if _, err := Parse(rel, []byte(out)); err != nil {
		return nil, err
	}
	return []byte(out), nil
}
