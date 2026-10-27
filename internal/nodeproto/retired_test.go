package nodeproto

import (
	"encoding/json"
	"testing"
)

// A node of an older version still declares the brain sandbox capability
// (sandbox, removed with the Docker Sandbox runtime for brains), and the
// core's registry may hold an approval of it. Both must stay harmless:
// they load, and the rest of the declaration counts as approved.
func TestRetiredSandboxCapabilityIsIgnored(t *testing.T) {
	old := []byte(`{"host":true,"fride":{"url":"http://127.0.0.1:17840"},"sandbox":{"version":"v0.50.0","signed_in":true,"daemon":true,"settings":true,"template":"fridolin-brain:v1","providers":["claude"]}}`)
	var declared, approved Capabilities
	if err := json.Unmarshal(old, &declared); err != nil {
		t.Fatalf("an old declaration: %v", err)
	}
	if err := json.Unmarshal(old, &approved); err != nil {
		t.Fatalf("an old approval: %v", err)
	}
	if err := declared.Check(); err != nil {
		t.Errorf("Check: %v", err)
	}
	if !Covers(approved, declared) {
		t.Error("the approval no longer covers the declaration")
	}
	if got := declared.Summary(); got != "host, fride" {
		t.Errorf("summary: %q", got)
	}
}
