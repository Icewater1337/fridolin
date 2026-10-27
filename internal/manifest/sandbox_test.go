package manifest

import (
	"strings"
	"testing"
)

// brain.json's sandbox takes the runtimes, fallbacks and host patterns
// it names, and nothing else.
func TestSandboxValidate(t *testing.T) {
	t.Parallel()
	ok := []*Sandbox{nil, {}, {Runtime: "docker-sandbox", Fallback: "refuse", Egress: []string{"api.github.com", "*.wikipedia.org", "**:443", "example.com:8080"}}, {Runtime: "bubblewrap"}}
	for _, s := range ok {
		m := Manifest{ID: "a", Name: "A", Sandbox: s}
		if err := m.Validate(); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	bad := []*Sandbox{{Runtime: "docker"}, {Fallback: "none"}, {Egress: []string{"https://x.org"}}, {Egress: []string{"a b"}}, {Egress: []string{".x.org"}}, {Egress: []string{"x.org:"}}, {Egress: make([]string, 51)}}
	for _, s := range bad {
		m := Manifest{ID: "a", Name: "A", Sandbox: s}
		if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "sandbox.") {
			t.Errorf("%+v passed: %v", s, err)
		}
	}
	if (&Sandbox{}).FallbackOf() != FallbackBubblewrap || (*Sandbox)(nil).RuntimeOf("") != RuntimeBubblewrap || (&Sandbox{}).RuntimeOf(RuntimeDockerSandbox) != RuntimeDockerSandbox {
		t.Error("the defaults")
	}
	strict := &Sandbox{Runtime: RuntimeDockerSandbox, Fallback: FallbackRefuse, Egress: []string{"a.org"}}
	loose := &Sandbox{Runtime: RuntimeBubblewrap, Egress: []string{"a.org", "b.org"}}
	got := strings.Join(Loosens(strict, loose, ""), "|")
	if got != "run under bubblewrap instead of a Docker Sandbox|reach these hosts from its Docker Sandbox: b.org" {
		t.Errorf("Loosens: %q", got)
	}
	if got := Loosens(strict, &Sandbox{Egress: []string{"a.org"}}, RuntimeDockerSandbox); len(got) != 1 || !strings.Contains(got[0], "instead of refusing") {
		t.Errorf("Loosens of the fallback: %q", got)
	}
	if got := Loosens(loose, strict, ""); len(got) != 0 {
		t.Errorf("a stricter sandbox loosens: %q", got)
	}
	empty := Manifest{ID: "a", Sandbox: &Sandbox{Egress: []string{}}}
	if empty.Normalized().Sandbox != nil {
		t.Error("an empty sandbox does not normalise away")
	}
}
