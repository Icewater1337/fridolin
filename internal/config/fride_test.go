package config

import "testing"

func TestFrideInstancesValidate(t *testing.T) {
	t.Parallel()
	ok := FrideInstanceConfig{Repos: []string{"https://gitlab.example.com/g/app.git"}, Identities: []string{"gitlab"}, Egress: []string{"registry.example.com"}, MemoryMB: 6144, IdleStopMinutes: -1}
	if err := validateFrideInstances(map[string]FrideInstanceConfig{"work": ok}); err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]map[string]FrideInstanceConfig{
		"native":          {"native": {}},
		"a capital":       {"Work": {}},
		"an identity":     {"work": {Identities: []string{"bitbucket"}}},
		"little memory":   {"work": {MemoryMB: 1024}},
		"an egress URL":   {"work": {Egress: []string{"https://x.example.com"}}},
		"a long idle":     {"work": {IdleStopMinutes: 2000}},
		"a repo with tab": {"work": {Repos: []string{"a\tb"}}},
	} {
		if err := validateFrideInstances(in); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	for v, want := range map[int]int{0: FrideIdleStopDefault, -1: 0, 5: 5} {
		if got := (FrideInstanceConfig{IdleStopMinutes: v}).IdleStop(); got != want {
			t.Errorf("IdleStop(%d) = %d", v, got)
		}
	}
}
