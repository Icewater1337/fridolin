package nodeagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// fakeMounts are the `sbx mount`s of sandbox name in the fake.
func fakeMounts(fake, name string) []string {
	b, _ := os.ReadFile(filepath.Join(fake, "mounts-"+name))
	return strings.FieldsFunc(string(b), func(r rune) bool { return r == '\n' })
}

// A brain's sessions are on the host: ensure binds their folder over the
// VM's after each create, also into a sandbox that stopped meanwhile;
// a rebuild and a remove keep the folder.
func TestSandboxHistory(t *testing.T) {
	t.Parallel()
	a, fake, _ := sandboxAgent(t)
	folder := brainFolder(t, "rt-1")
	p := nodeproto.SandboxEnsureParams{Brain: "work", Folder: folder, Marker: "rt-1", APIPort: 8765, Provider: "claude"}
	if _, perr := ensure(a, p); perr != nil {
		t.Fatal(perr)
	}
	host := filepath.Join(a.Dir, "sandbox", "history", "work", "claude")
	want := host + ":/home/agent/.claude/projects" // personal-data:ignore (the VM's home)
	if got := fakeMounts(fake, "fridolin-work"); len(got) != 1 || got[0] != want {
		t.Errorf("mounts %q, want %q", got, want)
	}
	if fi, err := os.Stat(host); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the host folder: %v %v", fi, err)
	}
	// A session written in the VM is in the host folder; a rebuild keeps it.
	if err := os.WriteFile(filepath.Join(host, "session.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The sandbox stopped before the mount: an exec starts it again.
	if err := os.WriteFile(filepath.Join(fake, "stopped-fridolin-work"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p.Egress = []string{"*.wikipedia.org"}
	if res, perr := ensure(a, p); perr != nil || res.State != nodeproto.SandboxRecreated {
		t.Fatalf("rebuild: %+v %v", res, perr)
	}
	if got := fakeMounts(fake, "fridolin-work"); len(got) != 1 {
		t.Errorf("mounts after the rebuild: %q", got)
	}
	if !called(fakeCalls(t, fake), "exec", "fridolin-work", "true") {
		t.Error("the stopped sandbox was not started for the mount")
	}
	if _, err := os.Stat(filepath.Join(host, "session.jsonl")); err != nil {
		t.Errorf("the rebuild lost the session: %v", err)
	}
	// A folder deleted on the host is made again, or sbx would not start.
	if err := os.RemoveAll(host); err != nil {
		t.Fatal(err)
	}
	if res, perr := ensure(a, p); perr != nil || res.State != nodeproto.SandboxReady {
		t.Fatalf("ensure: %+v %v", res, perr)
	}
	if _, err := os.Stat(host); err != nil {
		t.Errorf("the folder was not made again: %v", err)
	}
	if err := os.WriteFile(filepath.Join(host, "session.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, perr := sbCall(t, a, nodeproto.MethodSandboxRemove, nodeproto.SandboxRemoveParams{Brain: "work"}); perr != nil {
		t.Fatal(perr)
	}
	if _, err := os.Stat(filepath.Join(host, "session.jsonl")); err != nil {
		t.Errorf("removing the sandbox removed its sessions: %v", err)
	}
}

// The history folder is one name per brain in the state dir: an id that
// is a path, or no id, gets none.
func TestSandboxHistoryDirRefusesPaths(t *testing.T) {
	t.Parallel()
	a := &Agent{Dir: t.TempDir()}
	for _, brain := range []string{"", "..", "../x", "a/b", "/etc", "A"} {
		if dir, err := a.sandboxHistoryDir(brain, "claude"); err == nil {
			t.Errorf("%q: %s", brain, dir)
		}
	}
	if dir, err := a.sandboxHistoryDir("work", "evil/../x"); err != nil || filepath.Base(dir) != "claude" {
		t.Errorf("an unknown provider: %q %v", dir, err)
	}
	if _, err := (&Agent{}).sandboxHistoryDir("work", "claude"); err == nil {
		t.Error("an agent without a state dir has a history folder")
	}
}

// Codex keeps its sessions elsewhere; a mount that fails fails ensure,
// and the sandbox goes.
func TestSandboxHistoryCodexAndFailure(t *testing.T) {
	t.Parallel()
	a, fake, _ := sandboxAgent(t)
	if err := os.WriteFile(filepath.Join(fake, "codex"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	folder := brainFolder(t, "rt-1")
	p := nodeproto.SandboxEnsureParams{Brain: "cx", Folder: folder, Marker: "rt-1", APIPort: 8765, Provider: "codex"}
	if _, perr := ensure(a, p); perr != nil {
		t.Fatal(perr)
	}
	want := filepath.Join(a.Dir, "sandbox", "history", "cx", "codex") + ":/home/agent/.codex/sessions" // personal-data:ignore (the VM's home)
	if got := fakeMounts(fake, "fridolin-cx"); len(got) != 1 || got[0] != want {
		t.Errorf("mounts %q, want %q", got, want)
	}
	if err := os.WriteFile(filepath.Join(fake, "fail-mount"), []byte("error: mount: denied by policy"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.Brain = "other"
	_, perr := ensure(a, p)
	if perr == nil || perr.Code != nodeproto.CodeFailed || !strings.Contains(perr.Message, "denied by policy") {
		t.Fatalf("a mount that fails: %v", perr)
	}
	if !called(fakeCalls(t, fake), "rm", "-f", "fridolin-other") {
		t.Error("the sandbox whose mount failed stays")
	}
}
