//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexSandboxKeepsPersonalStateAndOtherBrainsOut(t *testing.T) {
	requireSandboxExec(t)
	cli := fakeCodex(t)
	script, err := os.ReadFile(cli)
	if err != nil {
		t.Fatal(err)
	}
	guards := `for secret in "$HOME/.codex/private" "$HOME/.agents/private" "$HOME/b/private"; do
 if cat "$secret" >/dev/null 2>&1; then echo 'unexpected read' >&2; exit 41; fi
done
if touch "$HOME/outside" 2>/dev/null; then exit 42; fi
echo safe > "$PWD/own-note"
`
	script = []byte(strings.Replace(string(script), "cat >", guards+"cat >", 1))
	if err := os.WriteFile(cli, script, 0700); err != nil {
		t.Fatal(err)
	}
	mgr, d, home := queueTestManager(t, 0, `,"agent_provider":"codex","codex_path":`+string(mustJSON(t, cli))+`,"dangerously_skip_permissions":true,"sandbox":{"enabled":true}`)
	for _, dir := range []string{".codex", ".agents", "b"} {
		p := filepath.Join(home, dir)
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "private"), []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "sandboxed Codex", func() bool { return d.count() == 1 })
	if res := d.all()[0].res; res.Err != nil || res.Text != "Codex reply" {
		t.Fatalf("got %+v", res)
	}
	if !fileExists(filepath.Join(home, "a", "own-note")) {
		t.Fatal("brain could not write its own folder")
	}
}
