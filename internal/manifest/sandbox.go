package manifest

import (
	"fmt"
	"slices"
	"strings"
)

// Sandbox is brain.json's "sandbox": where the brain runs, overriding
// config.json's sandbox.runtime, and what it may reach there
// (docs/SANDBOXES.md).
type Sandbox struct {
	// Runtime is RuntimeBubblewrap or RuntimeDockerSandbox; "" takes
	// config.json's sandbox.runtime.
	Runtime string `json:"runtime,omitempty"`
	// Fallback is what a run does when its Docker Sandbox is not
	// available: FallbackBubblewrap (the default) or FallbackRefuse.
	Fallback string `json:"fallback,omitempty"`
	// Egress are the hosts the brain may reach in its Docker Sandbox
	// beyond its provider and the core's API, in sbx's host patterns.
	Egress []string `json:"egress,omitempty"`
}

// The runtimes and the fallbacks.
const (
	RuntimeBubblewrap    = "bubblewrap"
	RuntimeDockerSandbox = "docker-sandbox"
	FallbackBubblewrap   = "bubblewrap"
	FallbackRefuse       = "refuse"
	// MaxEgress bounds sandbox.egress.
	MaxEgress = 50
)

// Runtimes and Fallbacks are the values the fields take.
func Runtimes() []string  { return []string{RuntimeBubblewrap, RuntimeDockerSandbox} }
func Fallbacks() []string { return []string{FallbackBubblewrap, FallbackRefuse} }

// FallbackOf is s's fallback, the default filled in.
func (s *Sandbox) FallbackOf() string {
	if s == nil || s.Fallback == "" {
		return FallbackBubblewrap
	}
	return s.Fallback
}

// RuntimeOf is the brain's runtime: its own, or dflt (config.json's).
func (s *Sandbox) RuntimeOf(dflt string) string {
	if s != nil && s.Runtime != "" {
		return s.Runtime
	}
	if dflt == "" {
		return RuntimeBubblewrap
	}
	return dflt
}

// validate reports what is wrong with brain id's sandbox.
func (s *Sandbox) validate(id string) error {
	if s == nil {
		return nil
	}
	if s.Runtime != "" && !slices.Contains(Runtimes(), s.Runtime) {
		return fmt.Errorf("brain %q: sandbox.runtime must be %q or %q, not %q", id, RuntimeBubblewrap, RuntimeDockerSandbox, s.Runtime)
	}
	if s.Fallback != "" && !slices.Contains(Fallbacks(), s.Fallback) {
		return fmt.Errorf("brain %q: sandbox.fallback must be %q or %q, not %q", id, FallbackBubblewrap, FallbackRefuse, s.Fallback)
	}
	if len(s.Egress) > MaxEgress {
		return fmt.Errorf("brain %q: sandbox.egress lists more than %d hosts", id, MaxEgress)
	}
	for _, h := range s.Egress {
		if !ValidEgressHost(h) {
			return fmt.Errorf("brain %q: sandbox.egress %q is not a host pattern (host, *.domain or **, an optional :port)", id, h)
		}
	}
	return nil
}

// Loosens says how next would let the brain run with less protection
// than prev, one plain clause each, for the owner's approval: a weaker
// runtime, a fallback to bubblewrap, hosts it could reach that it could
// not. dflt is config.json's sandbox.runtime.
func Loosens(prev, next *Sandbox, dflt string) []string {
	var out []string
	was, now := prev.RuntimeOf(dflt), next.RuntimeOf(dflt)
	if was == RuntimeDockerSandbox && now != RuntimeDockerSandbox {
		out = append(out, "run under bubblewrap instead of a Docker Sandbox")
	}
	if now == RuntimeDockerSandbox && was == RuntimeDockerSandbox && prev.FallbackOf() == FallbackRefuse && next.FallbackOf() == FallbackBubblewrap {
		out = append(out, "run under bubblewrap when its Docker Sandbox is not available, instead of refusing")
	}
	var wider []string
	for _, h := range next.egress() {
		if !slices.Contains(prev.egress(), h) {
			wider = append(wider, h)
		}
	}
	if len(wider) > 0 {
		out = append(out, "reach these hosts from its Docker Sandbox: "+strings.Join(wider, ", "))
	}
	return out
}

func (s *Sandbox) egress() []string {
	if s == nil {
		return nil
	}
	return s.Egress
}

// ValidEgressHost reports whether h is a host pattern sbx takes:
// letters, digits, dots, dashes and * (as in *.wikipedia.org or **:443),
// with an optional port.
func ValidEgressHost(h string) bool {
	host, port, hasPort := strings.Cut(h, ":")
	if host == "" || len(h) > 253 || hasPort && (port == "" || len(port) > 5 || strings.Trim(port, "0123456789") != "") {
		return false
	}
	return strings.Trim(host, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-*") == "" && !strings.HasPrefix(host, ".") && !strings.HasPrefix(host, "-")
}
