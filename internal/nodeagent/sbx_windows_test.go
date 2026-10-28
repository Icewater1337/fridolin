//go:build windows

package nodeagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// sbxUserSIDForTest is the SID of the user the tests run as.
func sbxUserSIDForTest(t *testing.T) string {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Sid.String()
}

// Run as a user, not as the node's service, the agent runs sbx itself:
// its pipes work, and nothing keeps it from running.
func TestSbxRunsAsTheUserOnWindows(t *testing.T) {
	t.Parallel()
	if sbxAsService() {
		t.Skip("the tests run as LocalSystem in session 0")
	}
	fake := t.TempDir()
	d := sbxCLI{path: testExe(t), env: []string{"FAKE_SBX_DIR=" + fake}}
	out, stderr, err := d.run(context.Background(), strings.NewReader(""), nil, "version")
	if err != nil || !strings.Contains(string(out), "sbx version") {
		t.Fatalf("%q %q %v", out, stderr, err)
	}
	if why := d.unavailable(); why != "" {
		t.Errorf("unavailable: %s", why)
	}
	if sbxInSession() {
		t.Error("in another session, run as a user")
	}
}

// A folder sbx reads or writes is the user's and SYSTEM's alone, and
// inherits nothing; what is made in it after is readable by the user.
func TestSbxHostFolderOnWindows(t *testing.T) {
	t.Parallel()
	if sbxAsService() {
		t.Skip("the tests run as LocalSystem in session 0")
	}
	dir := filepath.Join(t.TempDir(), "tokens")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (sbxCLI{}).hostFolder(dir); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	ctl, _, err := sd.Control()
	if err != nil || ctl&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("not protected: %v %v (%s)", ctl, err, sd)
	}
	sddl, sid := sd.String(), sbxUserSIDForTest(t)
	if strings.Count(sddl, "(A;") != 2 || !strings.Contains(sddl, ";;;SY)") || !strings.Contains(sddl, ";;;"+sid+")") {
		t.Errorf("the folder's ACL %s: not SYSTEM and %s alone", sddl, sid)
	}
	writeTestFile(t, filepath.Join(dir, "a.token"), "x")
	if b, err := os.ReadFile(filepath.Join(dir, "a.token")); err != nil || string(b) != "x" {
		t.Errorf("%q %v", b, err)
	}
}

// A token reaches sbx's secret through the command sbx runs on Windows,
// from a state dir whose path holds a space; the folder is the user's
// alone, and the token is in no argument of sbx's.
func TestHostTokensOnWindows(t *testing.T) {
	t.Parallel()
	if sbxAsService() {
		t.Skip("the tests run as LocalSystem in session 0")
	}
	fake := t.TempDir()
	dir := filepath.Join(t.TempDir(), "state dir", frideInstancesDir, "work", frideInstanceTokens)
	writeTestFile(t, filepath.Join(fake, "fride-fake"), "")
	saveFakeSandboxes(fake, []string{"fride-work"})
	d := sbxCLI{path: testExe(t), env: []string{"FAKE_SBX_DIR=" + fake}, self: testExe(t)}
	const gl = "glpat-madeUp-789" // personal-data:ignore (a made-up token)
	ph, err := setHostToken(context.Background(), d, dir, "fride-work", "gitlab.example.com", gl)
	if err != nil {
		t.Fatal(err)
	}
	if got := fakeCustom(fake, ph); got != "gitlab.example.com="+gl {
		t.Errorf("the custom secret: %q", got)
	}
	var cmd string
	for _, c := range fakeCalls(t, fake) {
		if len(c.Args) > 1 && c.Args[1] == "set-custom" {
			cmd = c.Args[len(c.Args)-1]
		}
	}
	if !strings.Contains(cmd, " node fride host sbx-secret ") || strings.Contains(cmd, `\`) {
		t.Errorf("sbx's command: %q", cmd)
	}
	raw, _ := os.ReadFile(filepath.Join(fake, "calls.log"))
	if strings.Contains(string(raw), "madeUp") {
		t.Error("the token is in sbx's arguments")
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if sddl := sd.String(); strings.Count(sddl, "(A;") != 2 {
		t.Errorf("the tokens' folder's ACL: %s", sddl)
	}
}

// A path with a space has a short form without one, where the volume
// keeps 8.3 names.
func TestShortPathOnWindows(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "with space")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	short := shortPath(dir)
	if short == dir {
		t.Skip("this volume keeps no 8.3 names")
	}
	if strings.Contains(short, " ") {
		t.Errorf("%q", short)
	}
	if fi, err := os.Stat(short); err != nil || !fi.IsDir() {
		t.Errorf("%q: %v", short, err)
	}
}
