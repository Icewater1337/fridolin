package main

import (
	"slices"
	"strings"
	"testing"
)

// groupSenderAllowed decides who in an allowed group reaches its brain:
// the numbers its group_members entry lists, everyone for ["*"], and
// without an entry whom allowed_numbers admits. A LID nothing maps to a
// number matches no number, and the bridge's own account always passes.
func TestGroupSenderAllowed(t *testing.T) {
	const (
		group  = testGroup
		other  = "120363000000000001@g.us"
		member = "15551234567"
	)
	yes, no := true, false
	number := func(n string) groupSender { return groupSender{Number: n, Resolved: true} }
	lid := func(n string) groupSender { return groupSender{Number: n} }
	fromMe := groupSender{Number: "5550100", Resolved: true, FromMe: true}

	cases := []struct {
		name    string
		numbers []string
		anyDM   *bool
		members map[string][]string
		sender  groupSender
		want    bool
	}{
		{"a listed member", nil, nil, map[string][]string{group: {member, otherNumber}}, number(member), true},
		{"a member the list leaves out", nil, nil, map[string][]string{group: {otherNumber}}, number(member), false},
		{"a list does not take allowed_numbers too", []string{member}, nil, map[string][]string{group: {otherNumber}}, number(member), false},
		{"a hand-written number on the list", nil, nil, map[string][]string{group: {"+1 555 123-4567"}}, number(member), true},
		{"a number one digit longer", nil, nil, map[string][]string{group: {member}}, number(member + "8"), false},
		{"another group's list", nil, nil, map[string][]string{other: {member}}, number(member), false},
		{"an empty list", []string{member}, nil, map[string][]string{group: {}}, number(member), false},
		{"* admits a stranger", nil, nil, map[string][]string{group: {anyMember}}, number(otherNumber), true},
		{"* admits a LID nobody knows", nil, nil, map[string][]string{group: {anyMember}}, lid("272727"), true},
		{"a LID never matches a listed number", nil, nil, map[string][]string{group: {member}}, lid(member), false},

		{"no entry: a number in allowed_numbers", []string{member}, nil, nil, number(member), true},
		{"no entry: a hand-written allowed number", []string{"+1 555 123 4567"}, nil, nil, number(member), true},
		{"no entry: a number allowed_numbers leaves out", []string{member}, nil, nil, number(otherNumber), false},
		{"no entry: a LID never matches an allowed number", []string{member}, nil, nil, lid(member), false},
		{"no entry: allow_any_dm does not widen a list", []string{member}, &yes, nil, number(otherNumber), false},
		{"no entry, nobody allowed", nil, &no, nil, number(member), false},
		{"no entry, anyone may DM: a stranger", nil, &yes, nil, number(otherNumber), true},
		{"no entry, anyone may DM: a LID nobody knows", nil, &yes, nil, lid("272727"), true},
		{"an entry is not widened by allow_any_dm", nil, &yes, map[string][]string{group: {member}}, number(otherNumber), false},

		{"the bridge's own account, no entry and nobody allowed", nil, &no, nil, fromMe, true},
		{"the bridge's own account, left off the list", nil, nil, map[string][]string{group: {member}}, fromMe, true},
		{"the bridge's own account, an empty list", nil, nil, map[string][]string{group: {}}, fromMe, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &Config{AllowedNumbers: c.numbers, AllowAnyDM: c.anyDM, AllowedGroups: []string{group, other}, GroupMembers: c.members}
			if got := groupSenderAllowed(cfg, group, c.sender); got != c.want {
				t.Errorf("groupSenderAllowed(%+v) = %v, want %v", c.sender, got, c.want)
			}
		})
	}
}

// FuzzGroupSenderAllowed: a group_members list admits a sender only when
// it lists their number, however either is written, and a LID's digits
// never pass for a number. allowed_numbers and allow_any_dm play no part
// once a group has an entry, and the bridge's own account always passes.
// The oracle for "lists their number" compares as the code does; what it
// checks without that is that a number listed as it comes, digits alone,
// always gets in, and an unresolved sender never does.
func FuzzGroupSenderAllowed(f *testing.F) {
	f.Add("15551234567", "15551234567", true, false)
	f.Add("15551234567", "+1 555 123 4567", true, false)
	f.Add("15551234567", "15551234567", false, false)
	f.Add("1555123456", "15551234567", true, true)
	f.Add("272727", "*", false, false)
	f.Add("", ",", true, true)
	f.Fuzz(func(t *testing.T, number, list string, resolved, anyDM bool) {
		var members []string
		if list != "" {
			members = strings.Split(list, ",")
		}
		cfg := &Config{AllowedGroups: []string{testGroup}, AllowAnyDM: &anyDM, GroupMembers: map[string][]string{testGroup: members}}
		got := groupSenderAllowed(cfg, testGroup, groupSender{Number: number, Resolved: resolved})
		if slices.Equal(members, []string{anyMember}) {
			if !got {
				t.Errorf("[%q] refused %q", anyMember, number)
			}
			return
		}
		canon := func(s string) string {
			if p, err := phoneNumber(s); err == nil {
				return p
			}
			return s
		}
		listed := resolved && slices.ContainsFunc(members, func(m string) bool { return canon(m) == canon(number) })
		if resolved && phoneNumberRE.MatchString(number) && slices.Contains(members, number) && !got {
			t.Errorf("group_members %q refused %q, which it lists as it is", members, number)
		}
		if !resolved && got {
			t.Errorf("group_members %q admitted the unresolved %q", members, number)
		}
		if got != listed {
			t.Errorf("group_members %q: %q (resolved %v) admitted: %v, listed: %v", members, number, resolved, got, listed)
		}
		cfg.AllowedNumbers = []string{number}
		if groupSenderAllowed(cfg, testGroup, groupSender{Number: number, Resolved: resolved}) != got {
			t.Errorf("group_members %q: allowed_numbers changes whether %q is admitted", members, number)
		}
		if !groupSenderAllowed(cfg, testGroup, groupSender{Number: number, Resolved: resolved, FromMe: true}) {
			t.Errorf("group_members %q: the bridge's own account is refused", members)
		}
	})
}

// group_members is read like allowed_numbers, its numbers kept as digits,
// and a config whose entries cannot mean what they say does not load.
func TestLoadConfigGroupMembers(t *testing.T) {
	const groups = `"allowed_groups": ["120363000000000000@g.us", "120363000000000001@g.us"]`
	cases := []struct {
		name    string
		members string
		want    map[string][]string
		err     string // part of the error; "" means it loads
	}{
		{"numbers as written", `{"120363000000000000@g.us": ["+1 555 123-4567", "15559876543"]}`,
			map[string][]string{testGroup: {"15551234567", "15559876543"}}, ""},
		{"everyone", `{"120363000000000000@g.us": ["*"], "120363000000000001@g.us": []}`,
			map[string][]string{testGroup: {anyMember}, "120363000000000001@g.us": {}}, ""},
		{"a group allowed_groups does not list", `{"120363000000000009@g.us": ["15551234567"]}`, nil, "120363000000000009@g.us is not in allowed_groups"},
		{"a chat that is no group", `{"15551234567@s.whatsapp.net": ["15551234567"]}`, nil, "a key is not a group JID"},
		{"a group JID with a name in it", `{"family@g.us": ["15551234567"]}`, nil, "a key is not a group JID"},
		{"a number that is none", `{"120363000000000000@g.us": ["15551234567", "call me"]}`, nil, "120363000000000000@g.us: entry 2 is not a phone number"},
		{"a LID", `{"120363000000000000@g.us": ["` + personLID + `@lid"]}`, nil, "entry 1 is not a phone number"},
		{"* with numbers", `{"120363000000000000@g.us": ["*", "15551234567"]}`, nil, `lists "*" with numbers`},
		{"* twice", `{"120363000000000000@g.us": ["*", "*"]}`, nil, `lists "*" with numbers`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			cfg, err := LoadConfig(writeConfig(t, `{"data_dir": "`+home+`/data", `+groups+`, "group_members": `+c.members+`}`))
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("LoadConfig: %v, want an error saying %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.GroupMembers) != len(c.want) {
				t.Fatalf("group_members = %q, want %q", cfg.GroupMembers, c.want)
			}
			for g, want := range c.want {
				if got, ok := cfg.GroupMembers[g]; !ok || !slices.Equal(got, want) {
					t.Errorf("group_members[%s] = %q, want %q", g, got, want)
				}
			}
		})
	}
}

// fridolin check --redact prints a config error as it is, masking only
// what it recognises as a number or a JID, so an error about group_members
// must not repeat an entry that is no number, or a key that is no group:
// either may be a phone number written in a way nothing recognises.
func TestGroupMembersErrorNamesNoEntry(t *testing.T) {
	cases := []struct {
		name    string
		members map[string][]string
		entry   string // what the error must not show, nor its digits
	}{
		{"a number with a note", map[string][]string{testGroup: {"15551234567", "+1 555 987 6543 (cell)"}}, "+1 555 987 6543 (cell)"},
		{"a number with a letter in it", map[string][]string{testGroup: {"1555987654x3"}}, "1555987654x3"},
		{"a LID", map[string][]string{testGroup: {"15559876543@lid"}}, "15559876543@lid"},
		{"a number for a key", map[string][]string{"15559876543": {"15551234567"}}, "15559876543"},
		{"a written number for a key", map[string][]string{"+1 555 987 6543": {"15551234567"}}, "+1 555 987 6543"},
		{"a number for a key, with the group's server", map[string][]string{"+1 555 987 6543@g.us": {"15551234567"}}, "+1 555 987 6543@g.us"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateGroupMembers(c.members, []string{testGroup})
			if err == nil {
				t.Fatalf("group_members %q was accepted", c.members)
			}
			if strings.Contains(err.Error(), c.entry) || strings.Contains(err.Error(), digitsOf(c.entry)) {
				t.Errorf("the error shows %q: %v", c.entry, err)
			}
		})
	}
}

// An edit that leaves an entry for a group no longer allowed is refused
// like a config file that has one, and changes nothing.
func TestConfigUpdateRefusesStrayGroupMembers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cs, err := OpenConfigStore(writeConfig(t, `{"data_dir": "`+home+`/data", "allowed_groups": ["120363000000000000@g.us"], "group_members": {"120363000000000000@g.us": ["15551234567"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = cs.Update("", func(c *Config) error {
		c.AllowedGroups = nil
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "not in allowed_groups") {
		t.Fatalf("Update: %v, want the stray entry refused", err)
	}
	if got := cs.Get().AllowedGroups; !slices.Equal(got, []string{testGroup}) {
		t.Errorf("allowed_groups = %q after a refused edit", got)
	}
}

// Each allowed group that admits only allowed_numbers is named once in
// the log, at startup and when an edit makes it so, with how to let every
// member in again; a group with an entry, or a bridge anyone may DM, is
// not.
func TestConfigWarnsAboutGroupsWithoutMembers(t *testing.T) {
	const (
		listed = "120363000000000001@g.us"
		added  = "120363000000000002@g.us"
	)
	home := t.TempDir()
	t.Setenv("HOME", home)
	logged := captureLog(t)
	cs, err := OpenConfigStore(writeConfig(t, `{"data_dir": "`+home+`/data", "allowed_numbers": ["15551234567"],
		"allowed_groups": ["120363000000000000@g.us", "`+listed+`"], "group_members": {"`+listed+`": ["15559876543"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	warned := func(g string) int {
		n := 0
		for _, line := range logged() {
			if strings.Contains(line, "group "+g+" has no group_members entry") {
				n++
				if !strings.Contains(line, `["*"]`) {
					t.Errorf("warning %q does not say how to let every member in", line)
				}
			}
		}
		return n
	}
	if warned(testGroup) != 1 || warned(listed) != 0 {
		t.Fatalf("at startup the log has:\n%s\nwant one warning, about %s", strings.Join(logged(), "\n"), testGroup)
	}

	// Another group allowed: only it is named.
	if _, err := cs.Update("", func(c *Config) error {
		c.AllowedGroups = append(c.AllowedGroups, added)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if warned(testGroup) != 1 || warned(added) != 1 {
		t.Errorf("after allowing %s the log has:\n%s\nwant it named once and %s not again", added, strings.Join(logged(), "\n"), testGroup)
	}
	// Its entry removed: the listed group is named now.
	if _, err := cs.Update("", func(c *Config) error {
		c.GroupMembers = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if warned(listed) != 1 {
		t.Errorf("after removing its entry the log has:\n%s\nwant %s named", strings.Join(logged(), "\n"), listed)
	}
}

// While anyone may DM, a group without an entry still lets every member
// in, so there is nothing to warn about.
func TestNoGroupWarningWhileAnyoneMayDM(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logged := captureLog(t)
	if _, err := OpenConfigStore(writeConfig(t, `{"data_dir": "`+home+`/data", "allowed_numbers": [], "allow_any_dm": true, "allowed_groups": ["120363000000000000@g.us"]}`)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(logged(), "\n"); strings.Contains(got, "group_members") {
		t.Errorf("warned although every member still gets in:\n%s", got)
	}
}

// Who was let in or out of which group is a security change like any
// other, and the log says so.
func TestLogSecurityChangesNamesGroupMembers(t *testing.T) {
	const other = "120363000000000001@g.us"
	logged := captureLog(t)
	prev := &Config{GroupMembers: map[string][]string{testGroup: {"15551234567"}}}
	next := &Config{GroupMembers: map[string][]string{testGroup: {"15559876543"}, other: {}}}
	logSecurityChanges(prev, next)
	got := strings.Join(logged(), "\n")
	for _, want := range []string{"group_members", "+" + testGroup + ":15559876543", "+" + other + ":", "-" + testGroup + ":15551234567"} {
		if !strings.Contains(got, want) {
			t.Errorf("log %q lacks %q", got, want)
		}
	}
}
