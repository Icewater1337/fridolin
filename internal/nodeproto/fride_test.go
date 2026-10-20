package nodeproto

import (
	"strings"
	"testing"
)

func TestFrideCapability(t *testing.T) {
	f := &FrideCap{URL: "http://127.0.0.1:7777", Repos: []string{"api"}}
	declared := Capabilities{Host: true, Fride: f}
	if err := declared.Check(); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"http://10.0.0.1:7777", "ftp://127.0.0.1", "http://user:pw@127.0.0.1:1", "", "http://example.com"} {
		if err := (Capabilities{Fride: &FrideCap{URL: u}}).Check(); err == nil {
			t.Errorf("%q: no error", u)
		}
	}
	for _, u := range []string{"http://localhost:7777", "https://[::1]:7777", "http://127.0.0.2:1"} {
		if err := CheckLoopbackURL(u); err != nil {
			t.Errorf("%q: %v", u, err)
		}
	}
	if Covers(Capabilities{Host: true}, declared) {
		t.Error("fride is covered without being approved")
	}
	// Approved once, it is covered whatever FrIDE reports later.
	approved := Capabilities{Host: true, Fride: &FrideCap{URL: "http://127.0.0.1:1"}}
	if !Covers(approved, declared) {
		t.Error("an approved fride with another port is not covered")
	}
	eff := Intersect(declared, approved)
	if eff.Fride == nil || eff.Fride.URL != f.URL || !eff.Allows(MethodFrideTask) || !eff.Allows(MethodFrideDelete) {
		t.Errorf("effective: %+v", eff)
	}
	if Intersect(Capabilities{Host: true}, approved).Allows(MethodFrideStatus) {
		t.Error("fride is allowed while FrIDE does not answer")
	}
	if got := declared.Summary(); got != "host, fride" {
		t.Errorf("summary %q", got)
	}
	if (Capabilities{Fride: f}).Empty() {
		t.Error("fride alone is nothing")
	}
	for _, kind := range []string{"started", "question", "done", "failed"} {
		if (FrideEvent{Event: kind}).Kind() != kind {
			t.Errorf("%s", kind)
		}
	}
	if !strings.Contains(strings.Join(Methods, " "), "fride.export") {
		t.Error("Methods lacks the fride methods")
	}
}
