package nodeagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeDocker stands in for docker when FAKE_DOCKER_DIR is set: it
// records each call in docker.log; save writes the tag into the file.
func fakeDocker(dir string) int {
	args := os.Args[1:]
	appendFile(filepath.Join(dir, "docker.log"), strings.Join(args, " ")+"\n")
	if b, err := os.ReadFile(filepath.Join(dir, "fail-docker-"+args[0])); err == nil {
		fmt.Fprint(os.Stderr, string(b))
		return 1
	}
	if args[0] == "save" {
		_ = os.WriteFile(args[slices.Index(args, "-o")+1], []byte(args[1]), 0o600)
	}
	return 0
}

// setupFixture is a checkout, a state dir and the fakes' folder, with
// sbx as it is before fridolin: its default settings, no policy, no
// template.
func setupFixture(t *testing.T) (SandboxSetupOptions, string, *bytes.Buffer) {
	t.Helper()
	repo, fake := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{"container/sandbox/Dockerfile": "FROM x\n", "tools/toolkit.py": "print(1)\n"} {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"settings.json": `{"browser.enabled":true,"ssh.agentForwardingEnabled":true}`, "policy-unset": "", "templates.json": "[]"} {
		if err := os.WriteFile(filepath.Join(fake, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out := &bytes.Buffer{}
	exe := testExe(t)
	return SandboxSetupOptions{Dir: t.TempDir(), Sbx: exe, Docker: exe, Repo: repo, Out: out,
		env: []string{"FAKE_SBX_DIR=" + fake, "FAKE_DOCKER_DIR=" + fake}}, fake, out
}

func TestSandboxSetup(t *testing.T) {
	t.Parallel()
	o, fake, out := setupFixture(t)
	if err := SandboxSetup(context.Background(), o); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	tag, err := SandboxTemplateTag(o.Repo)
	if err != nil || !strings.HasPrefix(tag, "fridolin-brain:") || len(tag) != len("fridolin-brain:")+12 {
		t.Fatalf("tag %q %v", tag, err)
	}
	a := &Agent{Dir: o.Dir}
	if st, err := a.loadSandboxState(); err != nil || st.Template != tag {
		t.Fatalf("state.json: %+v %v", st, err)
	}
	calls := fakeCalls(t, fake)
	for _, want := range [][]string{
		{"settings", "set", "browser.enabled", "false"}, {"settings", "set", "ssh.agentForwardingEnabled", "false"},
		{"policy", "init", "deny-all"}, {"daemon", "restart"}, {"template", "load"},
	} {
		if !called(calls, want...) {
			t.Errorf("no %v in %v", want, calls)
		}
	}
	docker, _ := os.ReadFile(filepath.Join(fake, "docker.log"))
	wantBuild := "build -f " + filepath.Join(o.Repo, "container", "sandbox", "Dockerfile") + " -t " + tag + " " + o.Repo
	if !strings.Contains(string(docker), wantBuild) || !strings.Contains(string(docker), "save "+tag+" -o ") {
		t.Errorf("docker: %s", docker)
	}
	if _, err := os.Stat(filepath.Join(o.Dir, "sandbox", "template.tar")); !os.IsNotExist(err) {
		t.Errorf("the image's tar stays: %v", err)
	}
	// Again: nothing to do.
	n, nd := len(calls), len(docker)
	if err := SandboxSetup(context.Background(), o); err != nil {
		t.Fatalf("setup again: %v", err)
	}
	for _, c := range fakeCalls(t, fake)[n:] {
		if len(c.Args) > 1 && slices.Contains([]string{"set", "init", "restart", "load"}, c.Args[1]) {
			t.Errorf("setup again ran %v", c.Args)
		}
	}
	if docker, _ := os.ReadFile(filepath.Join(fake, "docker.log")); len(docker) != nd {
		t.Errorf("setup again ran docker: %s", docker[nd:])
	}
	// A balanced policy: setup replaces it.
	if err := os.WriteFile(filepath.Join(fake, "policy"), []byte(fakeBalanced), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SandboxSetup(context.Background(), o); err != nil {
		t.Fatalf("setup over balanced: %v", err)
	}
	if !called(fakeCalls(t, fake), "policy", "reset", "--force") {
		t.Error("the balanced policy stays")
	}
}

func TestSandboxSetupRefuses(t *testing.T) {
	t.Parallel()
	o, fake, _ := setupFixture(t)
	if err := os.WriteFile(filepath.Join(fake, "signed-out"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SandboxSetup(context.Background(), o); !errors.Is(err, errSbxSignedOut) {
		t.Errorf("signed out: %v", err)
	}
	if err := os.Remove(filepath.Join(fake, "signed-out")); err != nil {
		t.Fatal(err)
	}
	// The build fails: no template recorded, so nothing declared.
	if err := os.WriteFile(filepath.Join(fake, "fail-docker-build"), []byte("no daemon"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SandboxSetup(context.Background(), o); err == nil {
		t.Error("a failed build set up")
	}
	if st, _ := (&Agent{Dir: o.Dir}).loadSandboxState(); st.Template != "" {
		t.Errorf("template recorded: %q", st.Template)
	}
	// Not a checkout.
	o.Repo = t.TempDir()
	if err := SandboxSetup(context.Background(), o); err == nil || !strings.Contains(err.Error(), "not a fridolin checkout") {
		t.Errorf("no checkout: %v", err)
	}
}

// --bin puts a stamped linux fridolin in place; an unstamped one is
// refused.
func TestSandboxSetupBin(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "fridolin")
	if err := os.WriteFile(src, fakeBinary("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	version := "v2.0.0"
	a := &Agent{Dir: t.TempDir(), checkSandboxBin: func(string, string) (string, error) { return version, nil }}
	if v, err := a.putSandboxBinFile(src); err != nil || v != version || a.sandboxBinVersion() != version {
		t.Fatalf("put: %q %v; declared %q", v, err, a.sandboxBinVersion())
	}
	version = ""
	if _, err := a.putSandboxBinFile(src); err == nil || !strings.Contains(err.Error(), "no version") {
		t.Errorf("unstamped: %v", err)
	}
}
