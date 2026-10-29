package nodeagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
)

// On Windows the token is sealed in the state dir under a key from the
// node's device key: it reads back, never in the clear, and not with
// another node's key. Elsewhere it is the host's store.
func TestFrideSecretStore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, _, err := FrideSecretStore("windows", func(string) string { return "" }, t.TempDir(), dir); err == nil {
		t.Fatal("a state dir without a key gave a store")
	}
	if _, err := NewKey(dir); err != nil {
		t.Fatal(err)
	}
	s, place, err := FrideSecretStore("windows", func(string) string { return "" }, t.TempDir(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(place, filepath.Join(dir, nodeSecretsDir)) {
		t.Errorf("place %q", place)
	}
	const tok = "sk-ant-oat01-madeUp_node-123" // personal-data:ignore (a made-up token)
	if err := s.Set(hostsecrets.ClaudeOAuthToken, tok); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(hostsecrets.ClaudeOAuthToken); err != nil || got != tok {
		t.Fatalf("read back %q, %v", got, err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, nodeSecretsDir, "*"))
	for _, f := range files {
		if data, _ := os.ReadFile(f); strings.Contains(string(data), tok) {
			t.Errorf("%s holds the token in the clear", f)
		}
	}
	other := t.TempDir()
	if _, err := NewKey(other); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(other, nodeSecretsDir), os.DirFS(filepath.Join(dir, nodeSecretsDir))); err != nil {
		t.Fatal(err)
	}
	o, _, _ := FrideSecretStore("windows", func(string) string { return "" }, t.TempDir(), other)
	if _, err := o.Get(hostsecrets.ClaudeOAuthToken); err == nil {
		t.Error("another node's key opens the token")
	}
	if s, place, err := FrideSecretStore("linux", func(string) string { return "" }, t.TempDir(), dir); err != nil || s == nil || place == "" {
		t.Errorf("linux: %v %q %v", s, place, err)
	}
}

// The fix names the command, on Windows with the state dir and an
// administrator PowerShell.
func TestClaudeTokenFix(t *testing.T) {
	t.Parallel()
	if got := claudeTokenFix("windows", `C:\Users\you\.fridolin-node`); got != `in an administrator PowerShell there: fridolin node secrets set claude.oauth.token --state-dir "C:\Users\you\.fridolin-node", with the token claude setup-token prints` {
		t.Errorf("windows: %q", got)
	}
	if got := claudeTokenFix("darwin", "/Users/you/fridolin-node"); got != "there: fridolin node secrets set claude.oauth.token, with the token claude setup-token prints" {
		t.Errorf("darwin: %q", got)
	}
}
