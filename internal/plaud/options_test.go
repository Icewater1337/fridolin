package plaud

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A waiting command chooses its login home only after the prior CLI command
// releases the token lock. The injected runner must observe that same policy.
func TestPlaudRunnerResolvesHomeAfterWaiting(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var current atomic.Pointer[Snapshot]
	current.Store(&Snapshot{})
	found := make(chan struct{})
	called := make(chan string, 1)
	p := New(Options{
		PlaudDir: filepath.Join(root, "plaud"), Home: filepath.Join(root, "shared"),
		Settings: current.Load,
		FindCLI:  func(string) (string, error) { close(found); return "fake-plaud", nil },
		Runner: func(_ context.Context, bin, home string, _ io.Writer, args ...string) (string, error) {
			if bin != "fake-plaud" || len(args) != 1 || args[0] != "me" {
				t.Errorf("unexpected process request: %s %v", bin, args)
			}
			called <- home
			return "account", nil
		},
	})
	defer p.Stop()
	p.cliMu.Lock()
	done := make(chan error, 1)
	go func() { _, err := p.runCLI(t.Context(), "me"); done <- err }()
	select {
	case <-found:
	case <-time.After(5 * time.Second):
		p.cliMu.Unlock()
		t.Fatal("CLI discovery never ran")
	}
	select {
	case <-called:
		p.cliMu.Unlock()
		t.Fatal("runner bypassed CLI serialization")
	default:
	}
	current.Store(&Snapshot{Plaud: Settings{BrainAccess: true}})
	p.cliMu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not finish")
	}
	if got := <-called; got != p.options.Home {
		t.Fatalf("runner home=%s, want current shared home %s", got, p.options.Home)
	}
}

// Replacing process execution with a fake does not move the login confinement
// check out of the service. Logout keeps its exception so it can remove a link.
func TestPlaudInjectedRunnerKeepsLoginConfinement(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	p := New(Options{
		PlaudDir: filepath.Join(root, "plaud"), Settings: func() *Snapshot { return &Snapshot{} },
		FindCLI: func(string) (string, error) { return "fake-plaud", nil },
	})
	defer p.Stop()
	if err := os.MkdirAll(filepath.Dir(p.tokenPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, p.tokenPath()); err != nil {
		t.Fatal(err)
	}
	var calls []string
	p.options.Runner = func(_ context.Context, _, _ string, _ io.Writer, args ...string) (string, error) {
		calls = append(calls, args[0])
		return "", nil
	}
	for _, operation := range []string{"me", "login"} {
		if _, err := p.runCLI(t.Context(), operation); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("%s accepted an unsafe login: %v", operation, err)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("unsafe login reached runner: %v", calls)
	}
	if _, err := p.runCLI(t.Context(), "logout"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "logout" {
		t.Fatalf("logout did not keep its exception: %v", calls)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "unchanged" {
		t.Fatalf("outside login was changed: %v", err)
	}
}
