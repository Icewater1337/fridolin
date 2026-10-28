package nodeagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A second node on the machine (node.json's service) gets a service of
// its own, run from its own binary once that is there; the machine's
// node keeps fridolin-node.
func TestNodeServiceConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if c := NodeServiceConfig("darwin", "/opt/fridolin", dir); c.Name != ServiceLabelDarwin || c.Executable != "/opt/fridolin" {
		t.Fatalf("no node.json: %q %q", c.Name, c.Executable)
	}
	if err := SaveConfig(dir, Config{Core: "core:8766", Name: "mac-sbx", Service: "fride-sandbox"}); err != nil {
		t.Fatal(err)
	}
	for goos, want := range map[string]string{"darwin": "ai.fridolin.node.fride-sandbox", "linux": "fridolin-node-fride-sandbox", "windows": "fridolin-node-fride-sandbox"} {
		c := NodeServiceConfig(goos, "/opt/fridolin", dir)
		if c.Name != want || c.Executable != "/opt/fridolin" || !strings.Contains(c.DisplayName, "fride-sandbox") {
			t.Errorf("%s: %q %q %q", goos, c.Name, c.Executable, c.DisplayName)
		}
		if got := strings.Join(c.Arguments, " "); got != "node run --state-dir "+dir {
			t.Errorf("%s: arguments %q", goos, got)
		}
	}
	if err := os.MkdirAll(filepath.Dir(OwnBinary(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(OwnBinary(dir), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if c := NodeServiceConfig("darwin", "/opt/fridolin", dir); c.Executable != OwnBinary(dir) {
		t.Errorf("with its own binary: %q", c.Executable)
	}
	for _, bad := range []string{"Fride", "-x", "a b", strings.Repeat("a", 33), "../x"} {
		if err := (Config{Core: "c:1", Name: "n", Service: bad}).Check(); err == nil {
			t.Errorf("service %q passed", bad)
		}
	}
}

func TestServiceNames(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"Fride", "-x", "a b", strings.Repeat("a", 33), "../x"} {
		if err := (Config{Core: "c:1", Name: "n", Service: bad}).Check(); err == nil {
			t.Errorf("service %q passed", bad)
		}
	}
}
