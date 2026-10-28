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
	} {
		p := ok
		p.Identities = append([]FrideIdentity(nil), ok.Identities...)
		mutate(&p)
		if err := p.Check(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
