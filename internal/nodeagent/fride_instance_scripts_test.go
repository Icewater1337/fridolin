package nodeagent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The serve script hands FrIDE the port it serves on as FRIDE_PORT, so
// that its agents' `fride pr` calls that port and not :7777, also with a
// FrIDE that predates serve.port (#815). It runs the script as the VM
// would, with a fake FrIDE that records its environment and fake setsid,
// ip and socat.
func TestInstanceServeGivesFrideItsPort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the script runs in the VM's sh")
	}
	home, bin := t.TempDir(), t.TempDir()
	envOut := filepath.Join(t.TempDir(), "env")
	fride := filepath.Join(bin, "fride")
	writeScript(t, fride, `env >"`+envOut+`"; echo "$@" >>"`+envOut+`"`)
	writeScript(t, filepath.Join(bin, "setsid"), `exec "$@"`)
	writeScript(t, filepath.Join(bin, "ip"), `echo "1.1.1.1 via 10.0.0.1 dev eth0 src 10.0.0.2 uid 1000"`)
	writeScript(t, filepath.Join(bin, "socat"), `exit 0`)
	if err := os.MkdirAll(filepath.Join(home, "fride-companion", "home"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(frideInstanceServe, "f=/opt/fride/bin/fride", "f="+fride, 1)
	if script == frideInstanceServe {
		t.Fatal("the serve script no longer sets f=/opt/fride/bin/fride")
	}
	argv := frideInstanceScript(script, "17850", "17851")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
	out, _ := cmd.CombinedOutput() // it exits 1 once FrIDE ends, as it should
	b, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatalf("FrIDE never ran: %v\n%s", err, out)
	}
	lines := strings.Split(string(b), "\n")
	for _, want := range []string{"FRIDE_PORT=17850", "FRIDE_HOME=" + filepath.Join(home, "fride-companion", "home"), "serve --under-fridolin --port 17850"} {
		if !slices.Contains(lines, want) {
			t.Errorf("FrIDE ran without %q:\n%s", want, b)
		}
	}
}
