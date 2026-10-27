package nodeagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// update with a new template makes the sandbox anew from it and carries
// the workspaces, sessions, FrIDE's tasks and the login over; FrIDE runs
// again. A create that fails leaves them on the host, and the next
// update finishes.
func TestFrideSandboxUpdateTemplate(t *testing.T) {
	t.Parallel()
	o, fake, out := createdSandbox(t)
	ctx := context.Background()
	supervising(t, o)
	st, _ := LoadFrideSandbox(o.Dir)
	st.Run = true
	if err := saveFrideSandbox(o.Dir, *st); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "FrIDE to run", func() bool { return lines(fakeFile(fake, "serves.log")) == 1 })
	vm := func(p string) string { return filepath.Join(fake, "vmhome", filepath.FromSlash(p)) }
	kept := map[string]string{
		"fride-companion/workspaces/w1/x.go": "package x",
		".claude/settings.json":              `{"env":{}}`,
		".claude/projects/p/a.jsonl":         "one",
		fh + "/tasks.json":                   "[]",
	}
	for p, body := range kept {
		writeTestFile(t, vm(p), body)
	}
	writeTestFile(t, filepath.Join(o.Repo, filepath.FromSlash(frideSandboxDockerfile)), "FROM y\n")
	tag, _ := FrideSandboxTemplateTag(o.Repo)
	writeTestFile(t, filepath.Join(fake, "fail-create"), "no space left\n")
	err := FrideSandboxUpdate(ctx, o)
	if err == nil || !strings.Contains(err.Error(), "update puts it back") {
		t.Fatalf("update with a failing create: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(o.carryDir(), "fride-companion", "workspaces", "w1", "x.go")); string(b) != "package x" {
		t.Fatalf("the carry: %q", b)
	}
	if st, _ := LoadFrideSandbox(o.Dir); st.Template == tag || st.Run {
		t.Errorf("state after the failed update: %+v", st)
	}
	_ = os.Remove(filepath.Join(fake, "fail-create"))
	out.Reset()
	if err := FrideSandboxUpdate(ctx, o); err != nil {
		t.Fatalf("update again: %v\n%s", err, out)
	}
	for p, body := range kept {
		if b, _ := os.ReadFile(vm(p)); string(b) != body {
			t.Errorf("%s in the new VM: %q", p, b)
		}
	}
	st, _ = LoadFrideSandbox(o.Dir)
	if st.Template != tag || !st.Run || st.DockerDisk != "40g" || o.carrying() {
		t.Errorf("after update: %+v (carry left: %v)", st, o.carrying())
	}
	if !called(fakeCalls(t, fake), "create", "--name", FrideSandboxName, "--memory", "6g", "-t", tag) {
		t.Error("no sandbox from the new template")
	}
	waitFor(t, "FrIDE to run again", func() bool { return lines(fakeFile(fake, "serves.log")) >= 2 })
}
