package nodeproto

import (
	"encoding/json"
	"strings"
	"testing"
)

// A rule with approve asks the owner for every call it allows, unless a
// rule without it allows the call too: that one wins.
func TestExecNeedsApproval(t *testing.T) {
	c := Capabilities{Exec: &ExecCap{Allow: []ExecRule{
		{Name: "brew", Args: []string{"upgrade", "update", "--version"}, Approve: true},
		{Name: "brew", Args: []string{"--version", "list"}},
		{Name: "shutdown", Args: []string{"-r", "now"}, Exact: true, Approve: true},
		{Name: "open", Args: []string{"-a", "[A-Za-z ]+"}},
		{Name: "softwareupdate", Args: []string{"--list", "-l"}, Approve: true},
		{Name: "softwareupdate", Args: []string{"--install", "--all", "-i", "-a"}, Approve: true},
	}}}
	for _, tc := range []struct {
		argv []string
		asks bool
	}{
		{[]string{"brew", "upgrade"}, true},
		{[]string{"brew", "--version"}, false}, // the free rule wins
		{[]string{"brew", "list"}, false},
		{[]string{"shutdown", "-r", "now"}, true},
		{[]string{"open", "-a", "Safari"}, false},
		{[]string{"softwareupdate", "-l"}, true}, // two rules allow it, both ask
		{[]string{"softwareupdate", "-i", "-a"}, true},
	} {
		if err := c.PermitsExec(tc.argv); err != nil {
			t.Errorf("%q is refused: %v", tc.argv, err)
		}
		if got := c.ExecNeedsApproval(tc.argv); got != tc.asks {
			t.Errorf("%q: needs approval %v, want %v", tc.argv, got, tc.asks)
		}
	}
	// What no rule allows needs no approval: PermitsExec refuses it.
	for _, argv := range [][]string{{"brew", "uninstall", "x"}, {"shutdown", "-h", "now"}, {"sh", "-c", "id"}, {}} {
		if c.PermitsExec(argv) == nil {
			t.Errorf("%q is allowed", argv)
		}
		if c.ExecNeedsApproval(argv) {
			t.Errorf("%q needs approval", argv)
		}
	}
	if (Capabilities{}).ExecNeedsApproval([]string{"brew", "upgrade"}) {
		t.Error("no exec needs approval")
	}
	// A shell stays refused, approve or not (Check refuses to declare one).
	sh := Capabilities{Exec: &ExecCap{Allow: []ExecRule{{Name: "bash", Approve: true}}}}
	if sh.PermitsExec([]string{"bash", "-c", "id"}) == nil {
		t.Error("an approve rule lets a shell through")
	}
	if err := (Capabilities{Exec: &ExecCap{Allow: []ExecRule{{Name: "zsh", Approve: true}}}}).Check(); err == nil {
		t.Error("a shell with approve is declared")
	}
}

// Turning approve on or off changes the rule: the owner approves the
// node's declaration again, and until then the rule is not effective.
func TestApproveIsPartOfTheRule(t *testing.T) {
	free := Capabilities{Exec: &ExecCap{Allow: []ExecRule{{Name: "brew", Args: []string{"upgrade"}}, {Name: "uptime", Args: []string{}}}}}
	asks := Capabilities{Exec: &ExecCap{Allow: []ExecRule{{Name: "brew", Args: []string{"upgrade"}, Approve: true}, {Name: "uptime", Args: []string{}}}}}
	if !Covers(asks, asks) || !Covers(free, free) {
		t.Fatal("an approval does not cover itself")
	}
	if Covers(free, asks) {
		t.Error("turning approve on is covered by the approval without it")
	}
	if Covers(asks, free) {
		t.Error("turning approve off is covered by the approval with it")
	}
	// While the new declaration waits, the rule that changed is not
	// effective; the rest is.
	eff := Intersect(free, asks)
	if eff.PermitsExec([]string{"brew", "upgrade"}) == nil || eff.PermitsExec([]string{"uptime"}) != nil {
		t.Errorf("effective while approve off waits: %+v", eff.Exec)
	}
	eff = Intersect(asks, asks)
	if eff.PermitsExec([]string{"brew", "upgrade"}) != nil || !eff.ExecNeedsApproval([]string{"brew", "upgrade"}) {
		t.Errorf("effective once approved: %+v", eff.Exec)
	}
	// approve travels in node.json and the core's state, and is left out
	// when off, so that older declarations read the same.
	data, err := json.Marshal(asks.Exec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"approve":true`) || strings.Count(string(data), `"approve"`) != 1 {
		t.Errorf("JSON: %s", data)
	}
	var back ExecCap
	if err := json.Unmarshal(data, &back); err != nil || !back.Allow[0].Approve || back.Allow[1].Approve {
		t.Errorf("after %s: %+v %v", data, back.Allow, err)
	}
}

// The core's own rules (programs from the core) never ask, and keep
// working beside rules that do.
func TestCoreRulesBesideApprove(t *testing.T) {
	core := ExecProgram{Name: "/Users/you/tools/scrape", Args: []string{"--all"}}.Rule()
	if core.Approve {
		t.Fatal("a program from the core asks")
	}
	c := Capabilities{Exec: &ExecCap{Allow: []ExecRule{core, {Name: "/Users/you/tools/scrape", Approve: true}}}}
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	if !Covers(c, c) {
		t.Error("not covered by itself")
	}
	if c.PermitsExec([]string{"/Users/you/tools/scrape", "--all"}) != nil || c.ExecNeedsApproval([]string{"/Users/you/tools/scrape", "--all"}) {
		t.Error("the core's program asks, or is refused")
	}
	if !c.ExecNeedsApproval([]string{"/Users/you/tools/scrape", "--one"}) {
		t.Error("another call of the program does not ask")
	}
	bad := core
	bad.Approve = true
	if err := (Capabilities{Exec: &ExecCap{Allow: []ExecRule{bad}}}).Check(); err == nil {
		t.Error("a rule of the core that asks is declared")
	}
}

func TestExecRuleText(t *testing.T) {
	for _, tc := range []struct {
		r    ExecRule
		want string
	}{
		{ExecRule{Name: "git", Args: []string{"status", "log"}}, "git status | log"},
		{ExecRule{Name: "hostname", Args: []string{}}, "hostname (no arguments)"},
		{ExecRule{Name: "go"}, "go (any arguments)"},
		{ExecRule{Name: "/opt/backup", Args: []string{"--all"}, Exact: true, Core: true}, "/opt/backup --all (exactly)"},
		{ExecRule{Name: "shutdown", Args: []string{"-r", "now"}, Exact: true, Approve: true}, "shutdown -r now (exactly)"},
	} {
		if got := tc.r.Text(); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.r, got, tc.want)
		}
	}
}

// Plain says the programs that ask apart from those that run when asked.
func TestPlainSaysWhatAsks(t *testing.T) {
	c := Capabilities{Exec: &ExecCap{Allow: []ExecRule{
		{Name: "uptime", Args: []string{}}, {Name: "brew", Args: []string{"list"}},
		{Name: "brew", Args: []string{"upgrade"}, Approve: true}, {Name: "shutdown", Approve: true}, {Name: "brew", Args: []string{"update"}, Approve: true},
	}}}
	got := strings.Join(c.Plain(), "\n")
	want := "run these programs there when asked: uptime, brew\nrun these programs there only with your yes each time: brew, shutdown"
	if got != want {
		t.Errorf("Plain:\n%s\nwant:\n%s", got, want)
	}
	only := Capabilities{Exec: &ExecCap{Allow: []ExecRule{{Name: "shutdown", Approve: true}}}}
	if got := strings.Join(only.Plain(), "\n"); got != "run these programs there only with your yes each time: shutdown" {
		t.Errorf("only asking: %q", got)
	}
}
