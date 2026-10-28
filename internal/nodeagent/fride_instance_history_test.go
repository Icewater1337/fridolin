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

// Removing an instance saves the agents' sessions and FrIDE's state, not
// the workspaces; its next sandbox gets them back, owned by the VM's
// user, without FrIDE's old pid file; a later removal merges into what
// was saved.
func TestFrideInstanceHistory(t *testing.T) {
	t.Parallel()
	f := newInstFix(t)
	f.fride("work")
	f.start(workParams())
	o := f.a.instanceOps()
	ctx := context.Background()
	saved := func(p string) string {
		b, _ := os.ReadFile(filepath.Join(o.historyDir("work"), filepath.FromSlash(p)))
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
		writeTestFile(t, f.home("work", p), body)
	}
	if err := o.remove(ctx, "work", false); err != nil {
		t.Fatal(err)
	}
	if saved(".claude/projects/p1/a.jsonl") != "one" || saved(".codex/sessions/2026/s.jsonl") != "codex" || saved(fh+"/tasks.json") != "[]" {
		t.Errorf("saved:\n%s", f.log)
	}
	for _, p := range []string{"fride-companion/workspaces", ".claude/settings.json"} {
		if _, err := os.Stat(filepath.Join(o.historyDir("work"), p)); err == nil {
			t.Errorf("remove saved %s", p)
		}
	}
	if fi, err := os.Stat(frideInstanceDir(f.a.Dir, "work")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("the instance's folder: %v %v", fi, err)
	}
	f.fride("work")
	f.start(workParams())
	if b, _ := os.ReadFile(f.home("work", ".claude/projects/p1/a.jsonl")); string(b) != "one" || !strings.Contains(f.log.String(), "Put back") {
		t.Errorf("not put back: %q\n%s", b, f.log)
	}
	if _, err := os.Stat(f.home("work", fh+"/fride.pid")); err == nil {
		t.Error("the old pid file came back")
	}
	if got := f.vm("work", "owns.log"); !strings.HasPrefix(got, fakeVMHome+" .claude/projects ") || !strings.Contains(got, "fride-companion/home") {
		t.Errorf("own: %q", got)
	}
	if !called(fakeCalls(t, f.fake), "exec", "-u", "root", "fride-work") {
		t.Error("own did not run as root")
	}
	// The agent prunes a session and starts another: both are saved.
	_ = os.Remove(f.home("work", ".claude/projects/p1/a.jsonl"))
	writeTestFile(t, f.home("work", ".claude/projects/p2/b.jsonl"), "two")
	if err := o.remove(ctx, "work", false); err != nil {
		t.Fatal(err)
	}
	if saved(".claude/projects/p1/a.jsonl") != "one" || saved(".claude/projects/p2/b.jsonl") != "two" {
		t.Error("the second removal did not merge")
	}
}

// A failing save keeps the sandbox; removing without history saves
// nothing.
func TestFrideInstanceRemoveWithoutHistory(t *testing.T) {
	t.Parallel()
	f := newInstFix(t)
	f.fride("work")
	f.start(workParams())
	o := f.a.instanceOps()
	ctx := context.Background()
	writeTestFile(t, filepath.Join(fakeVM(f.fake, "fride-work"), "fail-list"), "the VM does not answer\n")
	if err := o.remove(ctx, "work", false); err == nil || !strings.Contains(err.Error(), "nothing removed") {
		t.Fatalf("remove with a failing save: %v", err)
	}
	if st, _ := loadFrideInstance(f.a.Dir, "work"); st == nil || called(fakeCalls(t, f.fake), "rm") {
		t.Fatal("the failing save removed the sandbox")
	}
	if err := o.remove(ctx, "work", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(o.historyDir("work")); err == nil {
		t.Error("removing without history saved history")
	}
}
