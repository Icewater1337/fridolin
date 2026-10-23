package sched

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Template is a built-in task a schedule names (Schedule.Template): a
// markdown file in examples/schedules, whose frontmatter says what it is
// and which vars it takes.
//
//	---
//	description: One line on what it does.
//	when: daily 06:00          (what the dashboard suggests)
//	report: worth              (likewise)
//	vars: repos, max_issues=3  (a var with = has a default)
//	---
//	The task, with {{repos}} and {{max_issues}} filled in.
type Template struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	When        string `json:"when,omitempty"`
	Report      string `json:"report,omitempty"`
	Risk        string `json:"risk,omitempty"`
	// Secrets are those a schedule of it needs, which the dashboard adds.
	Secrets []string `json:"secrets,omitempty"`
	// Vars are the vars it takes, in order, with their defaults ("" for
	// one it needs).
	Vars     []TemplateVar `json:"vars,omitempty"`
	Body     string        `json:"-"`
	Optional bool          `json:"optional"`
}

// TemplateVar is a var a template takes.
type TemplateVar struct {
	Name    string `json:"name"`
	Default string `json:"default,omitempty"`
	// Required is a var without a default, which a schedule must set.
	Required bool `json:"required"`
}

// Templates reads the templates in dir of fsys, by name, sorted.
func Templates(fsys fs.FS, dir string) ([]Template, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []Template
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || e.IsDir() || !slugRe.MatchString(name) {
			continue
		}
		data, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		t, err := ParseTask(name, string(data))
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b Template) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// ParseTask reads a task file, a template or a schedule's own prompt file:
// its frontmatter, when it has one, as Templates reads it, and its body.
func ParseTask(name, text string) (Template, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	t := Template{Name: name, Body: text}
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return t, nil
	}
	front, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return t, fmt.Errorf("template %s: its frontmatter has no end", name)
	}
	t.Body = strings.TrimLeft(body, "\n")
	for line := range strings.SplitSeq(front, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "description":
			t.Description = value
		case "when":
			t.When = value
		case "report":
			t.Report = value
		case "risk":
			t.Risk = value
		case "optional":
			t.Optional = value == "true"
		case "secrets":
			for s := range strings.SplitSeq(value, ",") {
				if s = strings.TrimSpace(s); s != "" {
					t.Secrets = append(t.Secrets, s)
				}
			}
		case "vars":
			for v := range strings.SplitSeq(value, ",") {
				v = strings.TrimSpace(v)
				if v == "" {
					continue
				}
				n, def, hasDef := strings.Cut(v, "=")
				t.Vars = append(t.Vars, TemplateVar{Name: strings.TrimSpace(n), Default: strings.TrimSpace(def), Required: !hasDef})
			}
		}
	}
	return t, nil
}

var varRe = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_]*)\s*\}\}`)

// Render fills body's {{name}}s from vars, then from the template's
// defaults (t may be the zero Template, for a prompt file). A var the
// body names that neither has is an error, so that a run never gets a
// task with a hole in it.
func Render(t Template, body string, vars map[string]string) (string, error) {
	defaults := map[string]string{}
	for _, v := range t.Vars {
		if v.Required {
			if strings.TrimSpace(vars[v.Name]) == "" {
				return "", fmt.Errorf("template %s needs the var %q", t.Name, v.Name)
			}
			continue
		}
		defaults[v.Name] = v.Default
	}
	var missing []string
	out := varRe.ReplaceAllStringFunc(body, func(m string) string {
		name := varRe.FindStringSubmatch(m)[1]
		if v, ok := vars[name]; ok {
			return v
		}
		if v, ok := defaults[name]; ok {
			return v
		}
		missing = append(missing, name)
		return m
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("the task names %s, which the schedule's vars do not set", strings.Join(slices.Compact(missing), ", "))
	}
	return out, nil
}
