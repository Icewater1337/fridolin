//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests run generated profiles under the real sandbox-exec, so they
// check what the sandbox enforces rather than what the profile text says.

func requireSandboxExec(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(sandboxExecPath); err != nil {
		t.Skipf("%s not available: %v", sandboxExecPath, err)
	}
	// Inside another sandbox (go test run from a sandboxed tool) sandbox-exec
	// cannot apply any profile and fails with "Operation not permitted", the
	// same words as a denied access: a test that only expects denials would
	// pass without testing anything.
	if out, err := exec.Command(sandboxExecPath, "-p", "(version 1)(allow default)", "/usr/bin/true").CombinedOutput(); err != nil {
		t.Skipf("%s cannot apply a profile here: %v: %s", sandboxExecPath, err, out)
	}
}

// TestSandboxedRunAppliesProfileInline runs a brain through invoke with the
// sandbox on: the CLI starts under sandbox-exec, cannot read another brain's
// folder, and still starts when TMPDIR is unwritable (which used to break
// the profile file and, before that, the sandbox itself).
func TestSandboxedRunAppliesProfileInline(t *testing.T) {
	requireSandboxExec(t)
	mgr, d, home := queueTestManager(t, 0, `, "sandbox": {"enabled": true}`)
	secret := filepath.Join(home, "b", "secret.txt")
	if err := os.WriteFile(secret, []byte("brain b only"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WCB_FAKE_READ", secret)

	tmp := t.TempDir()
	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tmp, 0o700) })
	t.Setenv("TMPDIR", tmp)

	if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the run", func() bool { return d.count() == 1 })
	res := d.all()[0].res
	if res.Err != nil {
		t.Fatalf("sandboxed run failed: %v", res.Err)
	}
	if !strings.Contains(res.Text, "read=denied") {
		t.Errorf("brain a's run = %q, want it denied brain b's file", res.Text)
	}
}
