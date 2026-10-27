package nodeproto

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestSandboxCapability(t *testing.T) {
	s := &SandboxCap{Version: "v0.50.0", SignedIn: true, Daemon: true, Settings: true, Providers: []string{"claude"}}
	declared := Capabilities{Host: true, Sandbox: s}
	if err := declared.Check(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []SandboxCap{{Version: "1 2"}, {Template: "-t"}, {Providers: []string{"gemini"}}, {Problem: strings.Repeat("x", MaxSandboxProblemLen+1)}} {
		if err := (Capabilities{Sandbox: &bad}).Check(); err == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
	if Covers(Capabilities{Host: true}, declared) {
		t.Error("sandbox is covered without being approved")
	}
	// Approved once, it is covered whatever sbx reports later.
	approved := Capabilities{Host: true, Sandbox: &SandboxCap{}}
	if !Covers(approved, declared) {
		t.Error("an approved sandbox is not covered when sbx's state changed")
	}
	eff := Intersect(declared, approved)
	if eff.Sandbox == nil || eff.Sandbox.Version != "v0.50.0" || !slices.Equal(eff.Sandbox.Providers, s.Providers) {
		t.Errorf("effective: %+v", eff.Sandbox)
	}
	for _, m := range SandboxMethods {
		if !eff.Allows(m) {
			t.Errorf("%s is not allowed", m)
		}
		if !slices.Contains(Methods, m) {
			t.Errorf("Methods lacks %s", m)
		}
	}
	if Intersect(Capabilities{Host: true}, approved).Allows(MethodSandboxRun) {
		t.Error("sandbox.run is allowed while the node does not declare it")
	}
	if (Capabilities{Sandbox: s}).Empty() {
		t.Error("sandbox alone is nothing")
	}
	for c, want := range map[*SandboxCap]string{
		s: "sandbox (claude)",
		{SignedIn: true, Daemon: true, Providers: []string{"claude", "codex"}}: "sandbox (claude, codex)",
		{SignedIn: true, Daemon: true}:                                         "sandbox (no login)",
		{SignedIn: true, Daemon: false, Providers: []string{"claude"}}:         "sandbox (not ready)",
		{SignedIn: true, Daemon: true, Problem: "no template"}:                 "sandbox (not ready)",
	} {
		if got := (Capabilities{Sandbox: c}).Summary(); got != want {
			t.Errorf("summary of %+v: %q, want %q", c, got, want)
		}
	}
	if !s.Ready("claude") || s.Ready("codex") || (&SandboxCap{Providers: []string{"claude"}}).Ready("claude") {
		t.Error("Ready")
	}
}

// The info answer is the capability's fields and the others, flat.
func TestSandboxInfoJSON(t *testing.T) {
	info := SandboxInfo{
		SandboxCap:   SandboxCap{Version: "v1", SignedIn: true, Providers: []string{"codex"}},
		Sandboxes:    []SandboxVM{{Name: "fridolin-a", Brain: "a", Status: "stopped", MemoryMB: 4096}},
		FreeMemoryMB: 1234, BinPath: "/opt/fridolin/bin/fridolin", ToolsPath: "/opt/fridolin/tools",
	}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":"v1","signed_in":true,"daemon":false,"settings":false,"providers":["codex"],` +
		`"sandboxes":[{"name":"fridolin-a","brain":"a","status":"stopped","memory_mb":4096}],` +
		`"free_memory_mb":1234,"bin_path":"/opt/fridolin/bin/fridolin","tools_path":"/opt/fridolin/tools"}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}

func TestSandboxEnsureCheck(t *testing.T) {
	ok := SandboxEnsureParams{
		Brain: "work-1", Folder: "/Users/you/brains/work", Marker: "abc123",
		Mounts:   []SandboxMount{{Path: "/Users/you/fridolin-private", ReadOnly: true}},
		MemoryMB: 4096, Egress: []string{"api.github.com", "*.wikipedia.org", "**:443", "example.com:443", "10.0.0.1:80"},
		APIPort: 8765, Provider: "claude",
	}
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	many := make([]string, MaxSandboxEgress+1)
	for i := range many {
		many[i] = "a.example"
	}
	mounts := make([]SandboxMount, MaxSandboxMounts+1)
	for i := range mounts {
		mounts[i] = SandboxMount{Path: "/m"}
	}
	for name, change := range map[string]func(*SandboxEnsureParams){
		"brain":         func(p *SandboxEnsureParams) { p.Brain = "../x" },
		"relative":      func(p *SandboxEnsureParams) { p.Folder = "brains/work" },
		"colon":         func(p *SandboxEnsureParams) { p.Folder = "/a:ro" },
		"comma":         func(p *SandboxEnsureParams) { p.Mounts = []SandboxMount{{Path: "/a,/b"}} },
		"dotdot":        func(p *SandboxEnsureParams) { p.Mounts = []SandboxMount{{Path: "/a/../b"}} },
		"mounts":        func(p *SandboxEnsureParams) { p.Mounts = mounts },
		"marker":        func(p *SandboxEnsureParams) { p.Marker = "" },
		"marker line":   func(p *SandboxEnsureParams) { p.Marker = "a\nb" },
		"memory":        func(p *SandboxEnsureParams) { p.MemoryMB = 100 },
		"egress count":  func(p *SandboxEnsureParams) { p.Egress = many },
		"egress comma":  func(p *SandboxEnsureParams) { p.Egress = []string{"a.com,b.com"} },
		"egress flag":   func(p *SandboxEnsureParams) { p.Egress = []string{"--force"} },
		"egress space":  func(p *SandboxEnsureParams) { p.Egress = []string{"a.com b.com"} },
		"port":          func(p *SandboxEnsureParams) { p.APIPort = 0 },
		"provider":      func(p *SandboxEnsureParams) { p.Provider = "gemini" },
		"egress scheme": func(p *SandboxEnsureParams) { p.Egress = []string{"https://a.com"} },
	} {
		p := ok
		change(&p)
		if p.Check() == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestSandboxRunCheck(t *testing.T) {
	ok := SandboxRunParams{
		Brain: "work", Run: "r5N9IX3dZ5Vdu", Folder: "/Users/you/brains/work",
		Argv: []string{"claude", "-p"}, Env: map[string]string{"BRIDGE_CHAT": "x"},
		Stdin: "hello", Token: "tok", APIPort: 8765, Provider: "codex", TimeoutS: 600,
	}
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*SandboxRunParams){
		"run":       func(p *SandboxRunParams) { p.Run = "a/b" },
		"run space": func(p *SandboxRunParams) { p.Run = "a b" },
		"argv":      func(p *SandboxRunParams) { p.Argv = nil },
		"argv0":     func(p *SandboxRunParams) { p.Argv = []string{""} },
		"nul":       func(p *SandboxRunParams) { p.Argv = []string{"a\x00"} },
		"env name":  func(p *SandboxRunParams) { p.Env = map[string]string{"A=B": "c"} },
		"node env":  func(p *SandboxRunParams) { p.Env = map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "x"} },
		"proxy env": func(p *SandboxRunParams) { p.Env = map[string]string{"NO_PROXY": "*"} },
		"api env":   func(p *SandboxRunParams) { p.Env = map[string]string{"FRIDOLIN_API": "http://x"} },
		"stdin":     func(p *SandboxRunParams) { p.Stdin = strings.Repeat("x", MaxSandboxStdin+1) },
		"token":     func(p *SandboxRunParams) { p.Token = "" },
		"token nl":  func(p *SandboxRunParams) { p.Token = "a\nb" },
		"timeout":   func(p *SandboxRunParams) { p.TimeoutS = -1 },
		"big":       func(p *SandboxRunParams) { p.Argv = []string{"x", strings.Repeat("y", MaxSandboxArgBytes)} },
	} {
		p := ok
		change(&p)
		if p.Check() == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if (SandboxReadParams{Run: "r1", WaitMS: MaxSandboxReadWaitMS + 1}).Check() == nil {
		t.Error("a read waits longer than the bound")
	}
	if (SandboxReadParams{Run: "r1", Offset: -1}).Check() == nil {
		t.Error("a negative offset")
	}
	if err := (SandboxReadParams{Run: "r1", Offset: 10, WaitMS: 20000}).Check(); err != nil {
		t.Error(err)
	}
	if (SandboxStopParams{Run: "r1", GraceS: MaxSandboxGraceS + 1}).Check() == nil {
		t.Error("a stop's grace past the bound")
	}
	if g := (SandboxStopParams{Run: "r1"}).Grace(); g.Seconds() != DefaultSandboxGraceS {
		t.Errorf("default grace %v", g)
	}
	if (SandboxRemoveParams{Brain: "-rf"}).Check() == nil {
		t.Error("remove takes a brain that is no id")
	}
	if SandboxName("work") != "fridolin-work" {
		t.Error(SandboxName("work"))
	}
}
