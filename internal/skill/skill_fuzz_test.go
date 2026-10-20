package skill

import (
	"reflect"
	"strings"
	"testing"
)

// FuzzParse feeds Parse what a brain could write into a skill file. It
// must never panic, and a policy WithPolicy writes into a file that
// parses must read back as written, the rest of the frontmatter kept.
func FuzzParse(f *testing.F) {
	f.Add(taxes)
	f.Add("# Skill: x\n")
	f.Add("---\nname: a\ndescription: b\nmetadata: {\"fridolin\": {\"risk\": \"write\", \"triggers\": [\"x y\", 'z']}}\n---\n")
	f.Add("---\na:\n- b: c\n  d: [e, {f: g}]\n- |\n  h\n---\n")
	f.Add("---\n\"k\": >-\n  a\n\n  b\n---")
	f.Fuzz(func(t *testing.T, data string) {
		s, err := Parse("skills/a/SKILL.md", []byte(data))
		if s.Policy.Risk != "" && !s.Policy.Risk.Valid() {
			t.Fatalf("an invalid risk got through: %q", s.Policy.Risk)
		}
		if err != nil {
			return
		}
		p := Policy{Risk: RiskWrite, Triggers: []string{"taxes, now", "a: b"}, Approve: []string{"submit"}}
		out, err := WithPolicy("skills/a/SKILL.md", []byte(data), p, "")
		if err != nil {
			return // a frontmatter it reads but cannot keep (an odd key, say)
		}
		back, err := Parse("skills/a/SKILL.md", out)
		if err != nil {
			t.Fatalf("WithPolicy wrote a file Parse refuses: %v\n%s", err, out)
		}
		if !reflect.DeepEqual(back.Policy, p.Normalized()) || (s.Frontmatter && (back.Name != s.Name || back.Description != strings.TrimSpace(s.Description))) {
			t.Fatalf("round trip: %+v\nfrom %+v\n%s", back, s, out)
		}
	})
}
