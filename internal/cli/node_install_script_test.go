package cli

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

// The install script of the !join reply's native line (container/node.sh)
// runs here against a node port that serves stand-ins for the core's
// builds, with Tailscale signed in by a stub: it fetches the build for
// this machine with the code, checks its SHA-256, installs it in the
// home folder and runs its node join with the flags passed through.

// installScriptCore serves a node port of version v2.0.0 whose builds
// are shell scripts that record their arguments in calls; sum names the
// SHA-256 the manifest gives each ("" for the right one).
func installScriptCore(t *testing.T, calls, sum string) (svc *nodes.Service, builds map[string]string) {
	t.Helper()
	bins := t.TempDir()
	builds = map[string]string{}
	var entries []string
	for _, p := range []string{"darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64"} {
		goos, goarch, _ := strings.Cut(p, "/")
		content := "#!/bin/sh\n# fridolin v2.0.0 for " + p + "\ncase \"$1\" in version) echo 'fridolin v2.0.0'; exit 0 ;; esac\nprintf '%s\\n' \"$*\" >>" + calls + "\n"
		builds[p] = content
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, _ = zw.Write([]byte(content))
		_ = zw.Close()
		file := "fridolin-" + goos + "-" + goarch + ".gz"
		if err := os.WriteFile(filepath.Join(bins, file), gz.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		s := sum
		if s == "" {
			h := sha256.Sum256([]byte(content))
			s = hex.EncodeToString(h[:])
		}
		entries = append(entries, fmt.Sprintf(`{"os":%q,"arch":%q,"file":%q,"sha256":%q,"size":%d}`, goos, goarch, file, s, len(content)))
	}
	manifest := fmt.Sprintf(`{"version":"v2.0.0","binaries":[%s]}`, strings.Join(entries, ","))
	if err := os.WriteFile(filepath.Join(bins, nodes.BinariesManifest), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, err := nodes.New(nodes.Options{Dir: t.TempDir(), Version: "v2.0.0", BinariesDir: bins, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Serve(ln); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	return svc, builds
}

// runInstallScript runs node.sh with args in an empty home, with a
// Tailscale that is signed in, and returns the home, its output and
// whether it succeeded.
func runInstallScript(t *testing.T, args ...string) (home, out string, ok bool) {
	t.Helper()
	home, stubs := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(stubs, "tailscale"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", append([]string{filepath.Join(repoDir, "container", "node.sh")}, args...)...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + stubs + ":/usr/bin:/bin", "LANG=C"}
	cmd.Dir = home
	b, err := cmd.CombinedOutput()
	return home, string(b), err == nil
}

func skipWithoutInstallScriptTools(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("node.sh is for macOS and Linux; node.ps1 is Windows's")
	}
	for _, tool := range []string{"/bin/sh", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s", tool)
		}
	}
}

func TestNodeInstallScriptInstallsTheCoresFridolinAndJoins(t *testing.T) {
	skipWithoutInstallScriptTools(t)
	calls := filepath.Join(t.TempDir(), "calls")
	svc, builds := installScriptCore(t, calls, "")
	code, _, err := svc.NewJoinCodeFrom(nodes.JoinedFromChat)
	if err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(t.TempDir(), "Fridolin")
	home, out, ok := runInstallScript(t, "--core", svc.Addr(), "--code", code, "--yes", "--root", shared+":rw", "--no-exec", "--no-service")
	if !ok {
		t.Fatalf("node.sh failed:\n%s", out)
	}
	bin := filepath.Join(home, ".local", "lib", "fridolin", "fridolin")
	got, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("nothing installed: %v\n%s", err, out)
	}
	if !strings.HasPrefix(string(got), "#!/bin/sh\n# fridolin v2.0.0 for "+runtime.GOOS+"/") || builds[strings.TrimPrefix(strings.SplitN(string(got), "\n", 3)[1], "# fridolin v2.0.0 for ")] != string(got) {
		t.Fatalf("installed %q", got)
	}
	if st, err := os.Stat(bin); err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("the binary is not executable: %v %v", st, err)
	}
	if link, err := os.Readlink(filepath.Join(home, ".local", "bin", "fridolin")); err != nil || link != bin {
		t.Fatalf("~/.local/bin/fridolin: %q %v", link, err)
	}
	ran, _ := os.ReadFile(calls)
	if want := "node join " + code + " --core " + svc.Addr() + " --root " + shared + ":rw --no-exec --no-service\n"; string(ran) != want {
		t.Fatalf("fridolin ran %q, want %q\n%s", ran, want, out)
	}
	if st, err := os.Stat(shared); err != nil || !st.IsDir() {
		t.Fatalf("the folder it lends was not made: %v", err)
	}
	if !strings.Contains(out, "SHA-256 ") || !strings.Contains(out, "reply !approve") {
		t.Fatalf("the output:\n%s", out)
	}
	// The download left the code for node join to spend.
	if valid, err := svc.Registry().JoinCodeValid(code); !valid || err != nil {
		t.Fatalf("the code after the script: %v %v", valid, err)
	}

	// Again, with a fridolin there: the one it replaces is kept.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", filepath.Join(repoDir, "container", "node.sh"), "--core", svc.Addr(), "--code", code, "--yes", "--no-service")
	stubs := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubs, "tailscale"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd.Env = []string{"HOME=" + home, "PATH=" + stubs + ":/usr/bin:/bin", "LANG=C"}
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node.sh again: %v\n%s", err, b)
	}
	if prev, _ := os.ReadFile(bin + ".prev"); string(prev) != "#!/bin/sh\necho old\n" {
		t.Fatalf("fridolin.prev: %q", prev)
	}
}

func TestNodeInstallScriptRefusals(t *testing.T) {
	skipWithoutInstallScriptTools(t)
	calls := filepath.Join(t.TempDir(), "calls")
	svc, _ := installScriptCore(t, calls, "")
	code, _, err := svc.NewJoinCodeFrom(nodes.JoinedFromChat)
	if err != nil {
		t.Fatal(err)
	}
	forged, _ := installScriptCore(t, calls, strings.Repeat("0", 64))
	forgedCode, _, err := forged.NewJoinCodeFrom(nodes.JoinedFromChat)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"an unknown code", []string{"--core", svc.Addr(), "--code", "fj-" + strings.Repeat("a", 24)}, "the core answered 403: the join code is unknown, used or expired"},
		{"a build that does not match its SHA-256", []string{"--core", forged.Addr(), "--code", forgedCode}, "does not match the SHA-256"},
		{"no core", []string{"--code", code}, "--core is missing"},
		{"a core without a port", []string{"--core", "fridolin-core.example.ts.net", "--code", code}, "has no port"},
		{"a core that is not an address", []string{"--core", "core:8766/x?y", "--code", code}, "is not HOST:PORT"},
		{"not a code", []string{"--core", svc.Addr(), "--code", "../../x"}, "is not a join code"},
		{"a box flag", []string{"--core", svc.Addr(), "--code", code, "--box"}, "box.sh makes one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, out, ok := runInstallScript(t, append(tc.args, "--yes", "--no-service")...)
			if ok || !strings.Contains(out, tc.want) {
				t.Fatalf("ok %v, want %q in:\n%s", ok, tc.want, out)
			}
			if _, err := os.Stat(filepath.Join(home, ".local", "lib", "fridolin", "fridolin")); err == nil {
				t.Fatal("it installed fridolin")
			}
		})
	}
	if ran, _ := os.ReadFile(calls); len(ran) != 0 {
		t.Fatalf("fridolin ran: %q", ran)
	}
	if valid, _ := svc.Registry().JoinCodeValid(code); !valid {
		t.Fatal("a refusal spent the code")
	}
}

// The lines !join sends name the scripts' options, and the reply's hint
// names -Root and -NoExec for PowerShell: both scripts must take them.
func TestNodeInstallScriptsTakeTheOptionsTheReplyNames(t *testing.T) {
	sh, ps := httpapi.NativeCommands("fj-code", "core.example.ts.net:8766", httpapi.BoxSource{Source: "https://example.com/fridolin/abc"})
	shScript, err := os.ReadFile(filepath.Join(repoDir, "container", "node.sh"))
	if err != nil {
		t.Fatal(err)
	}
	psScript, err := os.ReadFile(filepath.Join(repoDir, "container", "node.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	// The options are the labels of node.sh's case on its arguments.
	options := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\t(--[-a-z |]+)\)`).FindAllStringSubmatch(string(shScript), -1) {
		for o := range strings.SplitSeq(m[1], " | ") {
			options[o] = true
		}
	}
	for _, f := range strings.Fields(sh) {
		if strings.HasPrefix(f, "--") && f != "--" && !options[f] {
			t.Errorf("node.sh does not take %s (it takes %v)", f, options)
		}
	}
	params := regexp.MustCompile(`(?m)^\s+\[(?:string|string\[\]|switch)\]\$(\w+)`).FindAllStringSubmatch(string(psScript), -1)
	took := map[string]bool{}
	for _, p := range params {
		took[p[1]] = true
	}
	for _, f := range append(strings.Fields(ps), "-Root", "-NoExec") {
		if name, ok := strings.CutPrefix(f, "-"); ok && name != "" && name[0] >= 'A' && name[0] <= 'Z' && !took[name] {
			t.Errorf("node.ps1 does not take %s (it takes %v)", f, took)
		}
	}
	if len(took) < 3 {
		t.Fatalf("node.ps1's parameters: %v", took)
	}
}
