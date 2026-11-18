package skill

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

const taxes = `---
name: taxes-de
description: >-
  File the German tax return with ELSTER,
  from the documents in the vault.
requires:
  env: [ELSTER_CERT]
  bins:
    - python3
os: [darwin, linux]
install:
  - kind: brew
    formula: python
  - pip install elster
metadata:
  fridolin:
    risk: irreversible   # submits
    vault: ["documents/taxes/**", documents/income/**]
    approve:
    - submit
    triggers: ["steuer", 'taxes', elster]
  other: {"openclaw": {"emoji": "x", "requires": {"bins": ["a", "b"]}}}
---
# Taxes

1. Gather.
`

func TestParseReadsTheFormat(t *testing.T) {
	s, err := Parse("skills/taxes-de/SKILL.md", []byte(taxes))
	if err != nil {
		t.Fatal(err)
	}
	want := Skill{
		Path: "skills/taxes-de/SKILL.md", Name: "taxes-de",
		Description: "File the German tax return with ELSTER, from the documents in the vault.",
		Requires:    Requires{Env: []string{"ELSTER_CERT"}, Bins: []string{"python3"}},
		OS:          []string{"darwin", "linux"},
		Install:     []map[string]string{{"kind": "brew", "formula": "python"}, {"run": "pip install elster"}},
		Policy: Policy{Risk: RiskIrreversible, Vault: []string{"documents/taxes/**", "documents/income/**"},
			Approve: []string{"submit"}, Triggers: []string{"steuer", "taxes", "elster"}},
		Frontmatter: true,
	}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("got  %+v\nwant %+v", s, want)
	}
	if !s.Matches("Can you do my Steuer?") || !s.Matches("ELSTER says no") || s.Matches("hello") {
		t.Error("triggers match case-insensitively as words")
	}
}

// A trigger counts only as a word or phrase of its own, so that a message
// that merely contains it does not come under the skill's policy (#388).
// Phrases and triggers with punctuation keep matching as before.
func TestMatchesWholeWords(t *testing.T) {
	cases := []struct {
		trigger, text string
		want          bool
	}{
		{"pay", "pay the bill", true},
		{"pay", "Can you PAY it?", true},
		{"pay", "please repay me", false},
		{"pay", "the payload is big", false},
		{"go", "good morning", false},
		{"go", "a week ago", false},
		{"go", "let's go!", true},
		// German compounds are words of their own.
		{"steuer", "Can you do my Steuererklärung?", false},
		{"steuererklärung", "STEUERERKLÄRUNG 2025, bitte", true},
		{"elster", "über ELSTER.", true},
		// Phrases match as written, at word boundaries.
		{"voice note", "Voice note, please", true},
		{"voice note", "here are my voice notes", false},
		{"pay the bill", "could you pay the bill today", true},
		{"pay the bill", "repay the bills", false},
		// Punctuation inside or at the edge of a trigger.
		{"[Plaud] New recording", `[Plaud] New recording "Team sync" (2026-11-18 09:00, 12:03).`, true},
		{"e-mail", "send an E-Mail to the bank", true},
		{"e-mail", "send e-mails", false},
		{"#invoice", "see #invoice", true},
		{"#invoice", "re#invoice", true}, // a symbol edge needs no boundary
		{"#invoice", "see #invoices", false},
		{".pdf", "the report.pdf attached", true},
		{"c++", "a c++20 build", true},
		{"c++", "abc++ code", false},
		{"x:y", "go x:y now", true},
		{"pay", "", false},
	}
	for _, c := range cases {
		s := Skill{Policy: Policy{Triggers: []string{c.trigger}}}
		if got := s.Matches(c.text); got != c.want {
			t.Errorf("trigger %q in %q: %v, want %v", c.trigger, c.text, got, c.want)
		}
	}
	if (Skill{}).Matches("pay") {
		t.Error("a skill without triggers matched")
	}
}

func TestParseWithoutFrontmatter(t *testing.T) {
	s, err := Parse("skills/transcription.md", []byte("# Skill: Transcription\n\nText.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Frontmatter || s.Name != "transcription" || s.Description != "Transcription" || s.Policy.Risk != RiskRead {
		t.Errorf("%+v", s)
	}
}

func TestParseRefusesWhatIsWrong(t *testing.T) {
	for _, tc := range []struct{ name, front, want string }{
		{"no name", "description: x", "name: is missing"},
		{"bad name", "name: Taxes DE\ndescription: x", "must be lower-case"},
		{"folder", "name: other\ndescription: x", `must be the name of its folder, "t"`},
		{"no description", "name: t", "description: is missing"},
		{"risk", "name: t\ndescription: x\nmetadata:\n  fridolin:\n    risk: dangerous", `"dangerous" is not read, write or irreversible`},
		{"unknown fridolin field", "name: t\ndescription: x\nmetadata:\n  fridolin:\n    rsik: read", "metadata.fridolin.rsik: is not a field"},
		{"vault outside", "name: t\ndescription: x\nmetadata:\n  fridolin:\n    vault: [../x]", "must not leave the vault"},
		{"vault absolute", "name: t\ndescription: x\nmetadata:\n  fridolin:\n    vault: [/etc]", "must be relative"},
		{"os", "name: t\ndescription: x\nos: [beos]", `"beos" is not darwin`},
		{"bins path", "name: t\ndescription: x\nrequires:\n  bins: [/bin/sh]", "not a path"},
		{"env", "name: t\ndescription: x\nrequires:\n  env: [A-B]", "not a variable name"},
		{"short trigger", "name: t\ndescription: x\nmetadata:\n  fridolin:\n    triggers: [a]", "too short"},
		{"tabs", "name: t\n\tdescription: x", "tabs"},
		{"duplicate", "name: t\nname: u", "given twice"},
		{"list root", "- a", "must be a mapping"},
		{"unclosed flow", "name: t\nos: [darwin", "expected , or ]"},
		{"anchor", "name: &a t", "anchors"},
		{"indent", "name: t\n  description: x", "unexpected indentation"},
	} {
		_, err := Parse("skills/t/SKILL.md", []byte("---\n"+tc.front+"\n---\nbody"))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	if _, err := Parse("skills/t.md", []byte("---\nname: t\n")); err == nil || !strings.Contains(err.Error(), "not closed") {
		t.Errorf("unclosed frontmatter: %v", err)
	}
}

func TestIsSkillPath(t *testing.T) {
	for p, want := range map[string]bool{
		"skills/a.md": true, "skills/a/SKILL.md": true, "skills/README.md": false,
		"skills/a/b.md": false, "memories/a.md": false, "skills/.md": false, "skills/a.txt": false,
	} {
		if IsSkillPath(p) != want {
			t.Errorf("IsSkillPath(%q) = %v", p, !want)
		}
	}
}

func TestWithPolicyRoundTrips(t *testing.T) {
	p := Policy{Risk: RiskWrite, Vault: []string{"documents/**"}, Approve: []string{"send mail"}, Triggers: []string{"mail, please", "x:y"}}
	out, err := WithPolicy("skills/taxes-de/SKILL.md", []byte(taxes), p, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Parse("skills/taxes-de/SKILL.md", out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !reflect.DeepEqual(s.Policy, p) || s.Name != "taxes-de" || !reflect.DeepEqual(s.Install[0], map[string]string{"kind": "brew", "formula": "python"}) {
		t.Errorf("%+v\n%s", s, out)
	}
	if !strings.HasSuffix(string(out), "---\n# Taxes\n\n1. Gather.\n") {
		t.Errorf("the body changed:\n%s", out)
	}
	// A file without a frontmatter gets one.
	out, err = WithPolicy("skills/Media Management.md", []byte("# Skill: Media\n\nbody\n"), Policy{Risk: RiskWrite}, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err = Parse("skills/Media Management.md", out)
	if err != nil || s.Name != "media-management" || s.Description != "Media" || s.Policy.Risk != RiskWrite {
		t.Errorf("%v %+v\n%s", err, s, out)
	}
}

func TestPolicyHash(t *testing.T) {
	a := Policy{Risk: RiskRead}
	if a.Hash() != (Policy{Risk: RiskRead, Vault: []string{}}).Hash() {
		t.Error("an empty list and none hash alike")
	}
	if a.Hash() == (Policy{Risk: RiskWrite}).Hash() {
		t.Error("a different risk hashes differently")
	}
}

func TestRisk(t *testing.T) {
	if Max(RiskRead, RiskIrreversible) != RiskIrreversible || Max(RiskWrite, RiskRead) != RiskWrite || Max("", "") != RiskRead {
		t.Error("Max")
	}
	if r, err := ParseRisk(" Write "); err != nil || r != RiskWrite {
		t.Error(r, err)
	}
}

func TestBlockScalars(t *testing.T) {
	n, err := ParseYAML("a: |\n  one\n  two\n\nb: >\n  x\n  y\n\n  z\nc: 'it''s'\nd: \"q\\\"\\u00e9\"\n")
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"a": "one\ntwo\n", "b": "x y\nz\n", "c": "it's", "d": "q\"é"} {
		if got := n.Get(k).Value; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// Names and descriptions made up for a file without a frontmatter fit
// the limits, so the file written parses (#627).
func TestWithPolicyCutsGeneratedValues(t *testing.T) {
	long := strings.Repeat("a", 63) + " " + strings.Repeat("b", 6) // slug cut right after the hyphen
	cases := map[string]string{
		"skills/" + long + ".md": "# Short\n",
		"skills/x.md":            "# " + strings.Repeat("d", 1100) + "\n",
		"skills/y.md":            "# " + strings.Repeat("d", 1023) + "é tail\n", // é across byte 1024
	}
	for rel, body := range cases {
		out, err := WithPolicy(rel, []byte(body), Policy{Risk: RiskRead}, "")
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		s, err := Parse(rel, out)
		if err != nil {
			t.Fatalf("%s: %v\n%s", rel, err, out)
		}
		if len(s.Name) > maxName || !nameRe.MatchString(s.Name) || strings.HasSuffix(s.Name, "-") {
			t.Errorf("%s: name %q", rel, s.Name)
		}
		if len(s.Description) > maxDescription || !utf8.ValidString(s.Description) || strings.Contains(s.Description, "…") {
			t.Errorf("%s: description %q", rel, s.Description)
		}
	}
}
