package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// testFridePin is the FrIDE commit the fixture's checkout pins.
const testFridePin = "a80b2268707fd4a580e9df6d25e5219d7cf406bd"

// frideSandboxFixture is a joined node's state dir, a checkout and the
// fakes' folder, with sbx running and signed in and no template yet.
func frideSandboxFixture(t *testing.T) (FrideSandboxOptions, string, *bytes.Buffer) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Docker Sandboxes run on macOS and Linux")
	}
	repo, fake, dir := t.TempDir(), t.TempDir(), t.TempDir()
	for name, body := range map[string]string{
		frideSandboxDockerfile: "FROM x\n",
		frideImageDockerfile:   "ARG FRIDE_REPO\nARG FRIDE_COMMIT=" + testFridePin + "\n",
		frideCompanionConfig:   `{"version":1,"name":"box","workspaceRoot":"/workspaces","baseFolders":["/var/lib/fride/repos"],"companion":{"enabled":true}}`,
	} {
		writeTestFile(t, filepath.Join(repo, filepath.FromSlash(name)), body)
	}
	writeTestFile(t, filepath.Join(fake, "templates.json"), "[]")
	if err := SaveConfig(dir, Config{Core: "core:8766", Name: "mac-sbx", Service: FrideSandboxService}); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	exe := testExe(t)
	return FrideSandboxOptions{Dir: dir, Sbx: exe, Docker: exe, Repo: repo, Base: filepath.Join(t.TempDir(), "repos"),
		FrideRepo: "https://github.com/you/fride", GitName: "You", GitEmail: "you@example.com", Out: out,
		env: []string{"FAKE_SBX_DIR=" + fake, "FAKE_DOCKER_DIR=" + fake}, url: "http://127.0.0.1:1", wait: time.Second}, fake, out
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeFile is a file of the fake VM's FrIDE, "" when it is not there.
func fakeFile(fake, name string) string {
	b, _ := os.ReadFile(filepath.Join(fake, "fride", name))
	return string(b)
}

// createdSandbox is the fixture after create.
func createdSandbox(t *testing.T) (FrideSandboxOptions, string, *bytes.Buffer) {
	t.Helper()
	o, fake, out := frideSandboxFixture(t)
	if err := FrideSandboxCreate(context.Background(), o); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	return o, fake, out
}

// create builds and loads the template, makes the sandbox with the base
// folder, its port, memory and egress, prepares FrIDE and builds it at the
// pinned commit; FrIDE does not run yet.
func TestFrideSandboxCreate(t *testing.T) {
	t.Parallel()
	o, fake, out := createdSandbox(t)
	st, err := LoadFrideSandbox(o.Dir)
	if err != nil || st == nil {
		t.Fatalf("state: %v %v", st, err)
	}
	base, _ := filepath.EvalSymlinks(o.Base)
	tag, _ := FrideSandboxTemplateTag(o.Repo)
	if st.Base != base || st.Template != tag || st.Commit != testFridePin || st.Repo != o.FrideRepo || st.Run || st.Built.IsZero() {
		t.Errorf("state %+v", st)
	}
	calls := fakeCalls(t, fake)
	for _, want := range [][]string{
		{"template", "load"},
		{"create", "--name", "fride-sandbox", "--memory", "6g", "-t", tag, "--pull", "never", "-p", "127.0.0.1:17850:17850", "claude", base},
		{"policy", "allow", "network", "--sandbox", "fride-sandbox", "localhost:17851"},
		{"policy", "deny", "network", "--sandbox", "fride-sandbox", "downloads.claude.ai"},
	} {
		if !called(calls, want...) {
			t.Errorf("no %v in %v", want, calls)
		}
	}
	egress := ""
	for _, c := range calls {
		if a := c.Args; len(a) == 6 && a[0] == "policy" && a[1] == "allow" && strings.Contains(a[5], ",") {
			egress = a[5]
		}
	}
	for _, h := range []string{"github.com:443", "proxy.golang.org:443", "sum.golang.org:443", "registry.npmjs.org:443", "api.anthropic.com:443"} {
		if !slices.Contains(strings.Split(egress, ","), h) {
			t.Errorf("egress %q lacks %s", egress, h)
		}
	}
	docker, _ := os.ReadFile(filepath.Join(fake, "docker.log"))
	if !strings.Contains(string(docker), "build -f "+filepath.Join(o.Repo, "container", "sandbox", "fride.Dockerfile")+" -t "+tag) {
		t.Errorf("docker: %s", docker)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(fakeFile(fake, "config.json")), &cfg); err != nil {
		t.Fatalf("FrIDE's config: %v", err)
	}
	if cfg["workspaceRoot"] != "~/fride-companion/workspaces" || !slices.Equal(anyStrings(cfg["baseFolders"]), []string{base}) || cfg["companion"] == nil {
		t.Errorf("FrIDE's config %v", cfg)
	}
	if fakeFile(fake, "git") != "You|you@example.com" || fakeFile(fake, "built") != testFridePin {
		t.Errorf("git %q, built %q", fakeFile(fake, "git"), fakeFile(fake, "built"))
	}
	if !strings.Contains(fakeFile(fake, "builds.log"), "https://github.com/you/fride "+testFridePin) || fakeFile(fake, "serves.log") != "" {
		t.Errorf("builds: %q, serves %q", fakeFile(fake, "builds.log"), fakeFile(fake, "serves.log"))
	}
	if !strings.Contains(out.String(), "fridolin node fride sandbox login") {
		t.Errorf("no next step in:\n%s", out)
	}
	if err := FrideSandboxCreate(context.Background(), o); err == nil || !strings.Contains(err.Error(), "made already") {
		t.Errorf("create again: %v", err)
	}
}

func anyStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, s := range l {
		str, _ := s.(string)
		out = append(out, str)
	}
	return out
}
