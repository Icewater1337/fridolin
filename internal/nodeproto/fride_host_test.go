package nodeproto

import (
	"encoding/json"
	"strings"
	"testing"
)

func frideHost() *FrideHostCap {
	return &FrideHostCap{Version: "v0.50.0", Ready: true, TotalMemoryMB: 24576, AvailableMemoryMB: 14000, ReserveMB: 6144,
		Instances: []FrideInstance{{Name: "fridolin", State: FrideInstanceRunning, MemoryMB: 6144, URL: "http://127.0.0.1:17850", Repos: []string{"fridolin"}}}}
}

func TestFrideHostCheck(t *testing.T) {
	t.Parallel()
	if err := frideHost().Check(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*FrideHostCap){
		"a name with capitals":        func(h *FrideHostCap) { h.Instances[0].Name = "Work" },
		"native":                      func(h *FrideHostCap) { h.Instances[0].Name = "native" },
		"twice":                       func(h *FrideHostCap) { h.Instances = append(h.Instances, h.Instances[0]) },
		"an unknown state":            func(h *FrideHostCap) { h.Instances[0].State = "paused" },
		"a URL elsewhere":             func(h *FrideHostCap) { h.Instances[0].URL = "http://192.0.2.1:17850" },
		"negative memory":             func(h *FrideHostCap) { h.AvailableMemoryMB = -1 },
		"a repository with a newline": func(h *FrideHostCap) { h.Instances[0].Repos = []string{"a\nb"} },
	} {
		h := frideHost()
		mutate(h)
		if err := h.Check(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// fride_host needs an approval of its own, grants the instance methods,
// and lets the fride methods reach an instance on a node without a
// native FrIDE.
func TestFrideHostApproval(t *testing.T) {
	t.Parallel()
	declared := Capabilities{FrideHost: frideHost()}
	if declared.Empty() {
		t.Error("fride_host counts as nothing to approve")
	}
	if Covers(Capabilities{}, declared) {
		t.Error("an approval without fride_host covers it")
	}
	approved := Capabilities{FrideHost: &FrideHostCap{}}
	if !Covers(approved, declared) {
		t.Error("an approval of fride_host does not cover a declaration with other instances")
	}
	eff := Intersect(declared, approved)
	if eff.FrideHost == nil || len(eff.FrideHost.Instances) != 1 {
		t.Fatalf("effective: %+v", eff.FrideHost)
	}
	eff.FrideHost.Instances[0].Repos[0] = "changed"
	if declared.FrideHost.Instances[0].Repos[0] != "fridolin" {
		t.Error("Intersect shares the declaration's instances")
	}
	for _, m := range append(FrideHostMethods, MethodFrideTask) {
		if !eff.Allows(m) {
			t.Errorf("%s not allowed", m)
		}
	}
	if (Capabilities{Fride: &FrideCap{URL: "http://127.0.0.1:17840"}}).Allows(MethodFrideInstanceStart) {
		t.Error("the fride capability allows fride.instance.start")
	}
	if got := eff.Summary(); got != "fride-host (1 instance)" {
		t.Errorf("summary %q", got)
	}
	if got := strings.Join(eff.Plain(), "; "); !strings.Contains(got, "FrIDE instances") {
		t.Errorf("plain: %s", got)
	}
}

// A fride call names its instance beside its own fields; an older core's
// call names none and reaches the node's own FrIDE.
func TestFrideOnInParams(t *testing.T) {
	t.Parallel()
	var p FrideTaskParams
	if err := json.Unmarshal([]byte(`{"message":"hi","instance":"work"}`), &p); err != nil || p.Instance != "work" || p.Message != "hi" {
		t.Fatalf("%+v %v", p, err)
	}
	b, err := json.Marshal(FrideTaskRef{TaskID: "t1"})
	if err != nil || string(b) != `{"task_id":"t1"}` {
		t.Errorf("%s %v", b, err)
	}
}

func TestFrideInstanceStartParamsCheck(t *testing.T) {
	t.Parallel()
	ok := FrideInstanceStartParams{Name: "work", MemoryMB: 6144, Repos: []string{"https://gitlab.example.com/g/app.git"},
		Identities: []FrideIdentity{{ID: "gitlab", Kind: "gitlab", Host: "gitlab.example.com", Token: "glpat-x"}}, Egress: []string{"registry.example.com"}}
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*FrideInstanceStartParams){
		"too little memory":        func(p *FrideInstanceStartParams) { p.MemoryMB = 1024 },
		"a bad name":               func(p *FrideInstanceStartParams) { p.Name = "-x" },
		"a repo with space":        func(p *FrideInstanceStartParams) { p.Repos = []string{"a b"} },
		"an identity without host": func(p *FrideInstanceStartParams) { p.Identities[0].Host = "" },
		"an egress path":           func(p *FrideInstanceStartParams) { p.Egress = []string{"example.com/x"} },
		"a template without a tag": func(p *FrideInstanceStartParams) { p.Template = "ghcr.io/you/fridolin-fride-sandbox" },
	} {
		p := ok
		p.Identities = append([]FrideIdentity(nil), ok.Identities...)
		mutate(&p)
		if err := p.Check(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// A template is an image on a registry, with a tag or a digest, and never
// an option of sbx.
func TestCheckTemplateRef(t *testing.T) {
	t.Parallel()
	digest := "@sha256:" + strings.Repeat("ab", 32)
	for _, ok := range []string{
		"ghcr.io/you/fridolin-fride-sandbox:v1.2.3-4-gabcdef12",
		"ghcr.io/you/fridolin-fride-sandbox:t-0123456789ab",
		"registry.example.com:5000/team/fridolin-fride-sandbox:main",
		"ghcr.io/you/fridolin-fride-sandbox" + digest,
		"ghcr.io/you/fridolin-fride-sandbox:v1" + digest,
	} {
		if err := CheckTemplateRef(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "fridolin-fride-sandbox:v1", "ghcr.io/you/fridolin-fride-sandbox", "--pull=always",
		"ghcr.io/you/Fridolin:v1", "ghcr.io/you/x:-v1", "ghcr.io/you/x:v1 y", "ghcr.io/you/x:" + strings.Repeat("a", 129),
	} {
		if err := CheckTemplateRef(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	p := FrideInstanceStartParams{Name: "work", MemoryMB: 4096, Template: "ghcr.io/you/fridolin-fride-sandbox:v1"}
	if err := p.Check(); err != nil {
		t.Error(err)
	}
	// Without one, the params are as an older core sends them.
	b, _ := json.Marshal(FrideInstanceStartParams{Name: "work", MemoryMB: 4096})
	if strings.Contains(string(b), "template") {
		t.Errorf("%s", b)
	}
}

// Asked leaves out what a node only reports, so that a FrIDE host whose
// free memory changes asks nothing new; what it asks still counts.
func TestAsked(t *testing.T) {
	t.Parallel()
	declare := func(mutate func(*Capabilities)) Capabilities {
		c := Capabilities{Host: true, FS: &FSCap{Roots: []FSRoot{{Path: "/srv/files", Write: true}}}, FrideHost: frideHost(),
			Fride: &FrideCap{URL: "http://127.0.0.1:17840", Repos: []string{"fridolin"}}, AndroidBuild: &AndroidBuildCap{Java: "21"},
			CoreHost: &CoreHostCap{Hash: "abc"}}
		if mutate != nil {
			mutate(&c)
		}
		return c
	}
	asked := func(c Capabilities) string {
		data, err := json.Marshal(c.Asked())
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	base := asked(declare(nil))
	for name, mutate := range map[string]func(*Capabilities){
		"free memory":       func(c *Capabilities) { c.FrideHost.AvailableMemoryMB = 9000 },
		"not ready":         func(c *Capabilities) { c.FrideHost.Ready, c.FrideHost.Problem = false, "sbx is not signed in" },
		"no instances":      func(c *Capabilities) { c.FrideHost.Instances = nil },
		"FrIDE's repos":     func(c *Capabilities) { c.Fride.Repos = nil },
		"Claude logged out": func(c *Capabilities) { c.Fride.NotLoggedIn = true },
		"the JDK":           func(c *Capabilities) { c.AndroidBuild.Java = "17" },
		"core-host":         func(c *Capabilities) { c.CoreHost = nil },
	} {
		if got := asked(declare(mutate)); got != base {
			t.Errorf("%s asks something else:\n%s\n%s", name, got, base)
		}
	}
	for name, mutate := range map[string]func(*Capabilities){
		"another root":  func(c *Capabilities) { c.FS.Roots = append(c.FS.Roots, FSRoot{Path: "/home/you"}) },
		"notify":        func(c *Capabilities) { c.Notify = true },
		"no FrIDE host": func(c *Capabilities) { c.FrideHost = nil },
		"no FrIDE":      func(c *Capabilities) { c.Fride = nil },
	} {
		if asked(declare(mutate)) == base {
			t.Errorf("%s asks the same", name)
		}
	}
	c := declare(nil)
	_ = c.Asked()
	if c.FrideHost.AvailableMemoryMB != 14000 || c.Fride.URL == "" || c.CoreHost == nil {
		t.Errorf("Asked changed the declaration: %+v", c)
	}
}
