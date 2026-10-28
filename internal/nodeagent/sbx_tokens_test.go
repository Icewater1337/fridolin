package nodeagent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeCustom is the custom secret with placeholder ph in the fake:
// "hosts=value", "" for none.
func fakeCustom(fake, ph string) string {
	b, _ := os.ReadFile(filepath.Join(fake, "custom", ph))
	return string(b)
}

// setHostToken stores the token on the host only, sets a sandbox-scoped
// custom secret for its placeholder and git's helper in the VM; for
// github.com also sbx's github service, on stdin. The token is in no
// argument of sbx's. removeHostToken undoes it.
func TestHostTokens(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Docker Sandboxes run on macOS and Linux")
	}
	fake, dir := t.TempDir(), filepath.Join(t.TempDir(), "tokens")
	writeTestFile(t, filepath.Join(fake, "fride-fake"), "")
	saveFakeSandboxes(fake, []string{"fride-work"})
	d := sbxCLI{path: testExe(t), env: []string{"FAKE_SBX_DIR=" + fake}}
	ctx := context.Background()
	const gh, gl = "gho_madeUpToken123", "glpat-madeUp-456" // personal-data:ignore (made-up tokens)
	ph, err := setHostToken(ctx, d, dir, "fride-work", "github.com", " "+gh+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if hosts := hostTokenHosts(dir); len(hosts) != 1 || hosts[0] != "github.com" {
		t.Fatalf("hosts %v", hosts)
	}
	tokFile, phFile, _ := hostTokenFiles(dir, "fride-work", "github.com")
	if b, _ := os.ReadFile(phFile); strings.TrimSpace(string(b)) != ph || !strings.HasPrefix(ph, "fridolin-git-") {
		t.Errorf("placeholder %q in %q", ph, b)
	}
	if fi, err := os.Stat(tokFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the token file: %v %v", fi, err)
	}
	if got := fakeCustom(fake, ph); got != "github.com,api.github.com="+gh {
		t.Errorf("the custom secret: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(fake, "github-secret")); string(got) != gh ||
		!called(fakeCalls(t, fake), "secret", "set", "github", "--sandbox", "fride-work", "--force") {
		t.Errorf("sbx's github secret: %q (replaced with --force, as sbx asks otherwise)", got)
	}
	if got := readFake(fake, "fride-work", "credentials"); got != "https://github.com x-access-token "+ph+"\n" {
		t.Errorf("git's helper in the VM: %q", got)
	}
	// Set again: the same placeholder, the new token.
	again, err := setHostToken(ctx, d, dir, "fride-work", "github.com", gh+"2")
	if err != nil || again != ph || fakeCustom(fake, ph) != "github.com,api.github.com="+gh+"2" {
		t.Errorf("again: %q %v (was %q): %q", again, err, ph, fakeCustom(fake, ph))
	}
	// Another host, with a port: GitLab's user.
	glPh, err := setHostToken(ctx, d, dir, "fride-work", "gitlab.example.com:8443", gl)
	if err != nil || glPh == ph || fakeCustom(fake, glPh) != "gitlab.example.com="+gl {
		t.Fatalf("gitlab: %q %v %q", glPh, err, fakeCustom(fake, glPh))
	}
	if !strings.Contains(readFake(fake, "fride-work", "credentials"), "https://gitlab.example.com:8443 oauth2 "+glPh) {
		t.Errorf("git's helper for GitLab: %q", readFake(fake, "fride-work", "credentials"))
	}
	raw, _ := os.ReadFile(filepath.Join(fake, "calls.log"))
	if strings.Contains(string(raw), "madeUp") {
		t.Error("a token is in sbx's arguments")
	}
	if err := removeHostToken(ctx, d, dir, "fride-work", "gitlab.example.com:8443"); err != nil {
		t.Fatal(err)
	}
	if fakeCustom(fake, glPh) != "" || strings.Contains(readFake(fake, "fride-work", "credentials"), "gitlab") {
		t.Errorf("gitlab after remove: %q %q", fakeCustom(fake, glPh), readFake(fake, "fride-work", "credentials"))
	}
	if hosts := hostTokenHosts(dir); len(hosts) != 1 {
		t.Errorf("hosts after remove: %v", hosts)
	}
	for _, bad := range [][3]string{{"fride-work", "https://github.com", gh}, {"fride-work", "GitHub.com", gh}, {"fride-work", "github.com", "two words"},
		{"fride-work", "github.com", ""}, {"fridolin-brain", "github.com", gh}} {
		if _, err := setHostToken(ctx, d, dir, bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("%q, %q with %q was taken", bad[0], bad[1], bad[2])
		}
	}
}

// readFake is a file of sandbox's FrIDE in the fake, "" when it is not
// there.
func readFake(fake, sandbox, file string) string {
	b, _ := os.ReadFile(filepath.Join(fakeVM(fake, sandbox), file))
	return string(b)
}
