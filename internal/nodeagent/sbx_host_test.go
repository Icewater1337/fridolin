package nodeagent

import (
	"bytes"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// sbx reads a token with /bin/cat on macOS and Linux, and with the
// node's fridolin.exe on Windows, by forward slashes, quoted only around
// a space; the fake sbx reads the file back out of either.
func TestSbxSecretCommand(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		goos, exe, file, want, back string
	}{
		{"darwin", "", "/srv/node/state/fride-instances/work/tokens/github.com.token", "/bin/cat '/srv/node/state/fride-instances/work/tokens/github.com.token'", "/srv/node/state/fride-instances/work/tokens/github.com.token"},
		{"windows", `C:\PROGRA~1\fridolin\fridolin.exe`, `C:\Users\you\AppData\Local\fridolin-node\fride-instances\work\tokens\github.com.token`,
			"C:/PROGRA~1/fridolin/fridolin.exe node fride host sbx-secret C:/Users/you/AppData/Local/fridolin-node/fride-instances/work/tokens/github.com.token",
			"C:/Users/you/AppData/Local/fridolin-node/fride-instances/work/tokens/github.com.token"},
		{"windows", `C:\Program Files\fridolin\fridolin.exe`, `C:\Users\you two\AppData\Local\fridolin-node\fride-instances\work\tokens\gitlab.example.com_8443.token`,
			"'C:/Program Files/fridolin/fridolin.exe' node fride host sbx-secret 'C:/Users/you two/AppData/Local/fridolin-node/fride-instances/work/tokens/gitlab.example.com_8443.token'",
			"C:/Users/you two/AppData/Local/fridolin-node/fride-instances/work/tokens/gitlab.example.com_8443.token"},
	} {
		got, err := sbxSecretCommandFor(c.goos, c.exe, c.file)
		if err != nil || got != c.want {
			t.Errorf("%s %s:\n got %q %v\nwant %q", c.goos, c.file, got, err, c.want)
		}
		want := c.back
		if c.goos == "windows" {
			want = filepath.FromSlash(want)
		}
		if back := sbxSecretFileOf(got); back != want {
			t.Errorf("%q read back as %q", got, back)
		}
	}
	for _, bad := range [][3]string{{"darwin", "", "/x/it's/a.token"}, {"windows", `C:\a"b\fridolin.exe`, `C:\t\a.token`}, {"windows", "", `C:\t\a.token`}} {
		if got, err := sbxSecretCommandFor(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("%q: %q", bad, got)
		}
	}
	if f := sbxSecretFileOf("/bin/cat /etc/passwd"); f != "" {
		t.Errorf("not one of ours: %q", f)
	}
}

// sbx-secret prints a token file of an instance's, and nothing else.
func TestPrintSbxSecret(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "fride-instances", "work", frideInstanceTokens)
	tok := filepath.Join(dir, "github.com.token")
	writeTestFile(t, tok, "ghs_madeUp") // personal-data:ignore (a made-up token)
	var out bytes.Buffer
	if err := PrintSbxSecret(&out, tok); err != nil || out.String() != "ghs_madeUp" {
		t.Errorf("%q %v", out.String(), err)
	}
	other := filepath.Join(filepath.Dir(dir), "fride.token")
	writeTestFile(t, other, "x")
	big := filepath.Join(dir, "big.token")
	writeTestFile(t, big, strings.Repeat("x", maxSbxSecret+1))
	for _, f := range []string{other, big, filepath.Join(dir, "missing.token"), filepath.Join(dir, "github.com.placeholder")} {
		if err := PrintSbxSecret(&out, f); err == nil {
			t.Errorf("%s: no error", f)
		}
	}
}

// The ACL of a folder sbx reads on Windows names SYSTEM and the user,
// and inherits nothing.
func TestSbxFolderSDDL(t *testing.T) {
	t.Parallel()
	if got := sbxFolderSDDL("S-1-5-21-1-2-3-1001"); got != "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;S-1-5-21-1-2-3-1001)" {
		t.Error(got)
	}
}

// sbx.exe is looked for on the user's PATH, then where winget links it.
func TestWindowsSbxCandidates(t *testing.T) {
	t.Parallel()
	env := []string{`Path=C:\Windows\system32;"C:\Program Files\Docker;x";;C:\Tools\`, `LOCALAPPDATA=C:\Users\you\AppData\Local`, `ProgramFiles=C:\Program Files`}
	want := []string{`C:\Windows\system32\sbx.exe`, `C:\Program Files\Docker;x\sbx.exe`, `C:\Tools\sbx.exe`,
		`C:\Users\you\AppData\Local\Microsoft\WinGet\Links\sbx.exe`, `C:\Program Files\WinGet\Links\sbx.exe`}
	if got := windowsSbxCandidates(env); !slices.Equal(got, want) {
		t.Errorf("%q", got)
	}
	if windowsEnv(env, "PATH") == "" || windowsEnv(env, "localappdata") != `C:\Users\you\AppData\Local` || windowsEnv(env, "HOME") != "" {
		t.Error("windowsEnv")
	}
	if runtime.GOOS != "windows" {
		return
	}
	// FindSbx takes the first that is a file.
	dir := t.TempDir()
	exe := filepath.Join(dir, "sbx.exe")
	writeTestFile(t, exe, "")
	if got := FindSbx([]string{"PATH=" + filepath.Join(dir, "none") + ";" + dir}); got != exe {
		t.Errorf("FindSbx: %q", got)
	}
	if got := FindSbx([]string{"PATH=" + filepath.Join(dir, "none")}); got != "" {
		t.Errorf("FindSbx without one: %q", got)
	}
}

func TestSbxSupported(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		goos, arch string
		want       bool
	}{{"darwin", "arm64", true}, {"linux", "amd64", true}, {"windows", "amd64", true}, {"windows", "arm64", false}, {"freebsd", "amd64", false}} {
		if got := sbxSupported(c.goos, c.arch); got != c.want {
			t.Errorf("%s/%s: %v", c.goos, c.arch, got)
		}
	}
}
