package nodeagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fh is FrIDE's state folder under the VM's home.
const fh = "fride-companion/home"

// rm saves the agents' sessions and FrIDE's state, not the workspaces;
// create puts them back, owned by the VM's user, without FrIDE's old pid
// file; a later rm merges into what was saved.
func TestFrideSandboxHistory(t *testing.T) {
	t.Parallel()
	o, fake, out := createdSandbox(t)
	ctx := context.Background()
	vm := func(p string) string { return filepath.Join(fake, "vmhome", filepath.FromSlash(p)) }
	saved := func(p string) string {
		b, _ := os.ReadFile(filepath.Join(o.historyDir(), filepath.FromSlash(p)))
		return string(b)
	}
	for p, body := range map[string]string{
		".claude/projects/p1/a.jsonl":        "one",
		".claude/history.jsonl":              "h",
		".codex/sessions/2026/s.jsonl":       "codex",
		fh + "/tasks.json":                   "[]",
		fh + "/fride.pid":                    "42",
		"fride-companion/workspaces/w1/x.go": "package x",
		".claude/settings.json":              `{"env":{}}`,
	} {
		writeTestFile(t, vm(p), body)
	}
	if err := FrideSandboxRemove(ctx, o); err != nil {
		t.Fatal(err)
	}
	if saved(".claude/projects/p1/a.jsonl") != "one" || saved(".codex/sessions/2026/s.jsonl") != "codex" || saved(fh+"/tasks.json") != "[]" {
		t.Errorf("saved: %q", out)
	}
	for _, p := range []string{"fride-companion/workspaces", ".claude/settings.json"} {
		if _, err := os.Stat(filepath.Join(o.historyDir(), p)); err == nil {
			t.Errorf("rm saved %s", p)
		}
	}
	if fi, err := os.Stat(filepath.Join(o.Dir, "fride-sandbox")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("the history's folder: %v %v", fi, err)
	}
	// A new VM is empty.
	if err := os.RemoveAll(filepath.Join(fake, "vmhome")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := FrideSandboxCreate(ctx, o); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(vm(".claude/projects/p1/a.jsonl")); string(b) != "one" || !strings.Contains(out.String(), "Put back") {
		t.Errorf("not put back: %q\n%s", b, out)
	}
	if _, err := os.Stat(vm(fh + "/fride.pid")); err == nil {
		t.Error("the old pid file came back")
	}
	if got := fakeFile(fake, "owns.log"); !strings.HasPrefix(got, fakeVMHome+" .claude/projects ") || !strings.Contains(got, "fride-companion/home") {
		t.Errorf("own: %q", got)
	}
	if !called(fakeCalls(t, fake), "exec", "-u", "root", FrideSandboxName) {
		t.Error("own did not run as root")
	}
	// The agent prunes a session and starts another: both are saved.
	_ = os.Remove(vm(".claude/projects/p1/a.jsonl"))
	writeTestFile(t, vm(".claude/projects/p2/b.jsonl"), "two")
	if err := FrideSandboxRemove(ctx, o); err != nil {
		t.Fatal(err)
	}
	if saved(".claude/projects/p1/a.jsonl") != "one" || saved(".claude/projects/p2/b.jsonl") != "two" {
		t.Error("the second rm did not merge")
	}
}

// rm --no-history saves nothing; a failing save keeps the sandbox.
func TestFrideSandboxRemoveWithoutHistory(t *testing.T) {
	t.Parallel()
	o, fake, _ := createdSandbox(t)
	ctx := context.Background()
	writeTestFile(t, filepath.Join(fake, "fride", "fail-list"), "the VM does not answer\n")
	if err := FrideSandboxRemove(ctx, o); err == nil || !strings.Contains(err.Error(), "nothing removed") {
		t.Fatalf("rm with a failing save: %v", err)
	}
	if st, _ := LoadFrideSandbox(o.Dir); st == nil || called(fakeCalls(t, fake), "rm") {
		t.Fatal("the failing save removed the sandbox")
	}
	o.SkipHistory = true
	if err := FrideSandboxRemove(ctx, o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(o.historyDir()); err == nil {
		t.Error("--no-history saved history")
	}
}
