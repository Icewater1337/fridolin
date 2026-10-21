package nodeproto

import "testing"

func TestWorkstationApproval(t *testing.T) {
	w := func(roots ...string) *WorkstationCap { return &WorkstationCap{Roots: roots} }
	approved := Capabilities{Agent: w("/home/you/Projects"), Terminal: w("/home/you/Projects")}
	if !Covers(approved, Capabilities{Agent: &WorkstationCap{Roots: []string{"/home/you/Projects"}, Providers: []string{ProviderClaude}, Sandbox: SandboxBwrap}}) {
		t.Error("what the node reports (providers, sandbox) needs no approval")
	}
	grown := Capabilities{Agent: w("/home/you/Projects", "/home/you/Other"), Terminal: w("/home/you/Projects")}
	if Covers(approved, grown) {
		t.Error("a new root was covered")
	}
	eff := Intersect(grown, approved)
	if !eff.Agent.AllowsFolder("/home/you/Projects/app") || eff.Agent.AllowsFolder("/home/you/Other") || eff.Agent.AllowsFolder("/home/you/Projects/../Other") {
		t.Errorf("effective: %+v", eff.Agent)
	}
	if Intersect(Capabilities{Agent: w("/x")}, approved).Agent != nil {
		t.Error("no root in common is no agent")
	}
	if !eff.Allows(MethodAgentStart) || !eff.Allows(MethodTerminalOpen) || (Capabilities{}).Allows(MethodTerminalRead) {
		t.Error("Allows")
	}
	if got := eff.Summary(); got != "agent (1 folder), terminal (1 folder)" {
		t.Errorf("summary %q", got)
	}
	for _, bad := range []Capabilities{
		{Agent: w()}, {Agent: w("relative")}, {Terminal: &WorkstationCap{Roots: []string{"/x"}, Sandbox: "docker"}},
		{Agent: &WorkstationCap{Roots: []string{"/x"}, Providers: []string{"gemini"}}},
	} {
		if bad.Check() == nil {
			t.Errorf("%+v passed the check", bad)
		}
	}
}

func TestAgentStartParamsCheck(t *testing.T) {
	ok := AgentStartParams{Folder: "/x", Prompt: "do it"}
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []AgentStartParams{
		{Folder: "x", Prompt: "p"}, {Folder: "/x", Prompt: " "}, {Folder: "/x", Prompt: "p", Provider: "gemini"},
		{Folder: "/x", Prompt: "p", Model: "--dangerously-skip-permissions"}, {Folder: "/x", Prompt: "p", CoreToken: "a b"},
	} {
		if p.Check() == nil {
			t.Errorf("%+v passed", p)
		}
	}
}
