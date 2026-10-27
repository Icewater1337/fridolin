package nodeagent

import (
	"context"
	"os"
	"path/filepath"
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
// argument of sbx's.
func TestFrideSandboxHostTokens(t *testing.T) {
	t.Parallel()
	o, fake, _ := createdSandbox(t)
	ctx := context.Background()
	const gh, gl = "gho_madeUpToken123", "glpat-madeUp-456" // personal-data:ignore (made-up tokens)
	if err := FrideSandboxGitHub(ctx, o, " "+gh+"\n"); err != nil {
		t.Fatal(err)
	}
	hosts := FrideSandboxHosts(o)
	if len(hosts) != 1 || hosts[0] != "github.com" {
		t.Fatalf("hosts %v", hosts)
	}
	tokFile, phFile, _ := hostTokenFiles(o.Dir, FrideSandboxName, "github.com")
	b, _ := os.ReadFile(phFile)
	ph := strings.TrimSpace(string(b))
	if fi, err := os.Stat(tokFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the token file: %v %v", fi, err)
	}
	if got := fakeCustom(fake, ph); got != "github.com,api.github.com="+gh {
		t.Errorf("the custom secret: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(fake, "github-secret")); string(got) != gh ||
		!called(fakeCalls(t, fake), "secret", "set", "github", "--sandbox", FrideSandboxName, "--force") {
		t.Errorf("sbx's github secret: %q (replaced with --force, as sbx asks otherwise)", got)
	}
	if got := fakeFile(fake, "credentials"); got != "https://github.com x-access-token "+ph+"\n" {
		t.Errorf("git's helper in the VM: %q", got)
	}
	// Set again: the same placeholder, the new token.
	again, err := FrideSandboxSetHostToken(ctx, o, "github.com", gh+"2")
	if err != nil || again != ph || fakeCustom(fake, ph) != "github.com,api.github.com="+gh+"2" {
		t.Errorf("again: %q %v (was %q): %q", again, err, ph, fakeCustom(fake, ph))
	}
	// Another host, with a port: its own egress, GitLab's user.
	glPh, err := FrideSandboxSetHostToken(ctx, o, "gitlab.example.com:8443", gl)
	if err != nil || glPh == ph || fakeCustom(fake, glPh) != "gitlab.example.com="+gl {
		t.Fatalf("gitlab: %q %v %q", glPh, err, fakeCustom(fake, glPh))
	}
	calls := fakeCalls(t, fake)
	if !called(calls, "policy", "allow", "network", "--sandbox", FrideSandboxName, "gitlab.example.com:8443") ||
		called(calls, "policy", "allow", "network", "--sandbox", FrideSandboxName, "github.com:443") {
		t.Error("the egress for the hosts")
	}
	if !strings.Contains(fakeFile(fake, "credentials"), "https://gitlab.example.com:8443 oauth2 "+glPh) {
		t.Errorf("git's helper for GitLab: %q", fakeFile(fake, "credentials"))
	}
	raw, _ := os.ReadFile(filepath.Join(fake, "calls.log"))
	if strings.Contains(string(raw), "madeUp") {
		t.Error("a token is in sbx's arguments")
	}
	if err := FrideSandboxRemoveHostToken(ctx, o, "gitlab.example.com:8443"); err != nil {
		t.Fatal(err)
	}
	if fakeCustom(fake, glPh) != "" || strings.Contains(fakeFile(fake, "credentials"), "gitlab") ||
		!called(fakeCalls(t, fake), "policy", "rm", "network", "--sandbox", FrideSandboxName, "--resource", "gitlab.example.com:8443", "--force") {
		t.Errorf("gitlab after remove: %q %q", fakeCustom(fake, glPh), fakeFile(fake, "credentials"))
	}
	if hosts := FrideSandboxHosts(o); len(hosts) != 1 {
		t.Errorf("hosts after remove: %v", hosts)
	}
	for _, bad := range [][2]string{{"https://github.com", gh}, {"GitHub.com", gh}, {"github.com", "two words"}, {"github.com", ""}} {
		if _, err := FrideSandboxSetHostToken(ctx, o, bad[0], bad[1]); err == nil {
			t.Errorf("%q with %q was taken", bad[0], bad[1])
		}
	}
	// A sandbox made anew gets the tokens again.
	if err := FrideSandboxRemove(ctx, o); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(fake, "github-secret"))
	if err := FrideSandboxCreate(ctx, o); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(fake, "github-secret")); string(got) != gh+"2" {
		t.Errorf("after create: sbx's github secret %q", got)
	}
}
