package manifest

import (
	"slices"
	"testing"
)

func list(s ...string) *[]string { return &s }

func workPolicy() Policy {
	m := Manifest{ID: "home", NodePolicy: []NodeRule{
		{Folder: "work", Storage: list("icebase"), Tasks: map[string][]string{TaskBackup: {"icebase"}, TaskCoding: {"icebase", "mac-mini"}}, Repos: []string{"acme-api"}},
		{Folder: "work/notes", Tasks: map[string][]string{TaskCoding: {"mac-mini"}}},
		{Folder: "/srv/secret", Tasks: map[string][]string{TaskExec: {}}},
	}}
	return Policy(m.FolderRules("/Users/you/Personal", "/Users/you"))
}

func TestNodePolicyDefaultIsUnrestricted(t *testing.T) {
	t.Parallel()
	var none Policy
	for _, kind := range NodeTaskKinds() {
		if l := none.Task(kind, "/anything"); l.Set || !l.Allows("any-node") {
			t.Errorf("no rules limit %s: %+v", kind, l)
		}
	}
	if l := none.Storage("/x"); l.Set || !l.Allows("cloud") {
		t.Errorf("no rules limit storage: %+v", l)
	}
	p := workPolicy()
	if l := p.Task(TaskCoding, "/Users/you/Personal/private"); l.Set {
		t.Errorf("a folder outside every rule is limited: %+v", l)
	}
	if l := p.Task(TaskExec, "/Users/you/Personal/work"); l.Set {
		t.Errorf("a kind the rule leaves out is limited: %+v", l)
	}
}

func TestNodePolicyClosestRuleWinsPerDimension(t *testing.T) {
	t.Parallel()
	p := workPolicy()
	notes := "/Users/you/Personal/work/notes/today.md"
	if l := p.Task(TaskCoding, notes); !l.Allows("mac-mini") || l.Allows("icebase") || l.Folder != "/Users/you/Personal/work/notes" {
		t.Errorf("the closer rule should win for coding: %+v", l)
	}
	// work/notes sets no storage: the parent's holds.
	if l := p.Storage(notes); !l.Allows("icebase") || l.Allows("cloud") || l.Allows("nas") {
		t.Errorf("the parent's storage should hold inside: %+v", l)
	}
	if l := p.Task(TaskExec, `/srv/secret/x`); !l.Set || l.Allows("icebase") {
		t.Errorf("an empty list allows nothing: %+v", l)
	}
	// A prefix that is no parent folder does not count.
	if l := p.Task(TaskCoding, "/Users/you/Personal/workshop"); l.Set {
		t.Errorf("workshop is not inside work: %+v", l)
	}
	if l := p.Task(TaskCoding, "/Users/you/Personal/work"); !l.Allows("ICEBASE") {
		t.Errorf("node names compare without case: %+v", l)
	}
}

func TestNodePolicyTwoBrainsOneFolderAllowWhatBothAllow(t *testing.T) {
	t.Parallel()
	p := Policy{
		{Brain: "a", Path: "/w", Rule: NodeRule{Tasks: map[string][]string{TaskCoding: {"x", "y"}}}},
		{Brain: "b", Path: "/w", Rule: NodeRule{Tasks: map[string][]string{TaskCoding: {"y", "z"}}}},
	}
	l := p.Task(TaskCoding, "/w/r")
	if !l.Allows("y") || l.Allows("x") || l.Allows("z") {
		t.Errorf("want only y: %+v", l)
	}
}

func TestNodePolicyWindowsPaths(t *testing.T) {
	t.Parallel()
	m := Manifest{ID: "b", NodePolicy: []NodeRule{{Folder: `D:\Work`, Tasks: map[string][]string{TaskExec: {"pc"}}}}}
	p := Policy(m.FolderRules("/core/b", "/home"))
	if l := p.Task(TaskExec, `d:\Work\repo`); !l.Set || l.Allows("mac") || !l.Allows("pc") {
		t.Errorf("a Windows path should match its folder: %+v", l)
	}
}

func TestNodePolicyRepos(t *testing.T) {
	t.Parallel()
	p := workPolicy()
	ls := p.RepoTask(TaskCoding, []string{"acme-api", "other"})
	if len(ls) != 1 || ls[0].Allows("gpu-box") || !ls[0].Allows("icebase") {
		t.Errorf("acme-api should inherit work's coding nodes: %+v", ls)
	}
	if ls := p.RepoTask(TaskCoding, []string{"other"}); len(ls) != 0 {
		t.Errorf("a repository no rule lists is not limited: %+v", ls)
	}
}

func TestNodePolicyBackupDenied(t *testing.T) {
	t.Parallel()
	p := workPolicy()
	all, _, pats := p.BackupDenied("/Users/you/Personal", "cloud")
	if all || !slices.Equal(pats, []string{"/work/"}) {
		t.Errorf("cloud: all %v, patterns %v; want /work/ kept out", all, pats)
	}
	if all, _, pats := p.BackupDenied("/Users/you/Personal", "icebase", "0123456789ab"); all || len(pats) != 0 {
		t.Errorf("icebase may take everything: all %v, %v", all, pats)
	}
	if all, why, _ := p.BackupDenied("/Users/you/Personal/work", "nas"); !all || why == "" {
		t.Errorf("a backup of work itself to nas is denied whole: %v %q", all, why)
	}
}

func TestValidateNodePolicy(t *testing.T) {
	t.Parallel()
	for _, bad := range [][]NodeRule{
		{{Folder: "../up"}},
		{{Folder: ".hidden"}},
		{{Folder: "/x/../y"}},
		{{Folder: "a"}, {Folder: "a"}},
		{{Folder: "a", Tasks: map[string][]string{"gaming": {"x"}}}},
		{{Folder: "a", Storage: list("")}},
		{{Folder: "a", Storage: list(" spaced ")}},
		{{Folder: "a", Repos: []string{"no/slash"}}},
	} {
		if err := validateNodePolicy(bad); err == nil {
			t.Errorf("%+v should be refused", bad)
		}
	}
	good := []NodeRule{{Folder: ""}, {Folder: "work/x", Storage: list()}, {Folder: `C:\Work`}, {Folder: "~/Personal/work", Tasks: map[string][]string{TaskBackup: {"cloud", "icebase"}}, Repos: []string{"acme.api"}}}
	if err := validateNodePolicy(good); err != nil {
		t.Error(err)
	}
}
