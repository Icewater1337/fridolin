package nodeagent

import (
	"context"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// The FrIDE template takes its base image, Claude Code and ponytail from
// the brain template and the fridolin-fride image, and this checkout pins
// FrIDE's commit.
func TestFrideSandboxDockerfilePins(t *testing.T) {
	t.Parallel()
	repo := filepath.Join("..", "..")
	anyValue := regexp.MustCompile(`^\S+$`)
	arg := func(file, name string) string {
		t.Helper()
		v, err := dockerfileArg(filepath.Join(repo, filepath.FromSlash(file)), name, anyValue)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for name, file := range map[string]string{
		"BASE_IMAGE": "container/sandbox/Dockerfile", "CLAUDE_CODE_VERSION": "container/sandbox/Dockerfile",
		"PONYTAIL_REPO": frideImageDockerfile, "PONYTAIL_COMMIT": frideImageDockerfile,
	} {
		if got, want := arg(frideSandboxDockerfile, name), arg(file, name); got != want {
			t.Errorf("%s: %s in %s, %s in %s", name, got, frideSandboxDockerfile, want, file)
		}
	}
	if _, err := FridePin(repo); err != nil {
		t.Error(err)
	}
	if _, err := companionConfig(repo, "/srv/repos"); err != nil {
		t.Error(err)
	}
}

// The scripts are sh, and the sandbox's name is never a brain's.
func TestFrideSandboxScripts(t *testing.T) {
	t.Parallel()
	if strings.HasPrefix(FrideSandboxName, nodeproto.SandboxName("")) {
		t.Errorf("%s is named like a brain's sandbox", FrideSandboxName)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for _, s := range []string{frideSandboxPrepare, frideSandboxGit, frideSandboxBuild, frideSandboxServe,
		frideSandboxStop, frideSandboxPairCode, frideSandboxLogin, frideSandboxStatus} {
		if out, err := exec.Command(sh, "-n", "-c", s).CombinedOutput(); err != nil || !strings.HasPrefix(s, "# fride-sandbox: ") {
			t.Errorf("%v %s\n%s", err, out, s)
		}
	}
}

func TestFrideRepoFrom(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"git@github.com:you/fridolin.git":          "https://github.com/you/fride",
		"https://github.com/you/fridolin":          "https://github.com/you/fride",
		"https://x-token@github.com/you/fridolin/": "https://github.com/you/fride", // personal-data:ignore (a made-up user in a URL)
		"ssh://git@github.com/you/fridolin.git":    "https://github.com/you/fride",
		"https://gitlab.com/you/fridolin":          "",
		"/srv/git/fridolin":                        "",
	} {
		got, err := frideRepoFrom(in)
		if got != want || (err == nil) != (want != "") {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
}

// A sandbox whose FrIDE does not build is removed again, and no state is
// left, so create can run again.
func TestFrideSandboxCreateFails(t *testing.T) {
	t.Parallel()
	o, fake, out := frideSandboxFixture(t)
	writeTestFile(t, filepath.Join(fake, "fride", "fail-build"), "go: no network\n")
	err := FrideSandboxCreate(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "no network") || !strings.Contains(err.Error(), "removed again") {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if !called(fakeCalls(t, fake), "rm", "-f", FrideSandboxName) {
		t.Error("the sandbox stayed")
	}
	if st, _ := LoadFrideSandbox(o.Dir); st != nil {
		t.Errorf("state %+v", st)
	}
	cfg, _ := LoadConfig(o.Dir)
	cfg.Fride = &FrideConfig{URL: "http://127.0.0.1:17840", Webhook: "127.0.0.1:17841"}
	if err := SaveConfig(o.Dir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := FrideSandboxCreate(context.Background(), o); err == nil || !strings.Contains(err.Error(), "node of its own") {
		t.Errorf("create on a node paired with another FrIDE: %v", err)
	}
}
