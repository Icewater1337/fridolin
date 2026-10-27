package toolcli

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// The brain client takes the proxy from the environment, which a Docker
// Sandbox needs (its proxy swaps the run token's placeholder), and still
// reaches a bridge on loopback directly.
func TestBrainClientUsesTheEnvironmentsProxy(t *testing.T) {
	t.Parallel()
	tok := filepath.Join(t.TempDir(), "run.token")
	if err := os.WriteFile(tok, []byte("t0ken"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FRIDOLIN_API": "http://127.0.0.1:8765", "FRIDOLIN_BRAIN_TOKEN_FILE": tok}
	c, err := BrainClientFor(IO{Getenv: func(k string) string { return env[k] }})
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatalf("the brain client's transport has no proxy function: %#v", c.http.Transport)
	}
	for _, u := range []string{"http://127.0.0.1:8765/brain/mcp/tools", "http://localhost:8765/brain/mcp/tools", "http://[::1]:8765/x"} {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			t.Fatal(err)
		}
		if p, err := tr.Proxy(req); err != nil || p != nil {
			t.Errorf("%s goes through the proxy %v (%v)", u, p, err)
		}
	}
}
