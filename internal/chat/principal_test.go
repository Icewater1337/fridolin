package chat

import "testing"

// These values already appear in persisted chat history. The zero value
// represents a caller without credentials, not a trusted script.
func TestPrincipalHistorySource(t *testing.T) {
	for _, c := range []struct {
		name string
		from Principal
		via  string
	}{
		{"zero", Principal{}, "api"},
		{"anonymous", Principal{Kind: CallerAnonymous}, "api"},
		{"script", Principal{Kind: CallerScript}, "script"},
		{"brain", Principal{Kind: CallerBrain, Brain: "notes"}, "brain"},
		{"unknown", Principal{Kind: PrincipalKind(-1)}, "api"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.from.Via(); got != c.via {
				t.Errorf("history source = %q, want %q", got, c.via)
			}
		})
	}
}
