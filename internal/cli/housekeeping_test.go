package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestHousekeepingUsageGolden(t *testing.T) {
	assertGolden(t, "cli/housekeeping-usage.golden", []byte(housekeepingUsage))
}

func TestHousekeepingCmdArguments(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{nil, 2, "fridolin housekeeping run"},
		{[]string{"--help"}, 0, "fridolin housekeeping status"},
		{[]string{"run", "extra"}, 2, "takes no arguments"},
		{[]string{"run", "--config", "/nonexistent/config.json"}, 1, "loading config"},
	} {
		var out, errb bytes.Buffer
		code := housekeepingCmd(tc.args, &out, &errb)
		if code != tc.code || !strings.Contains(out.String()+errb.String(), tc.want) {
			t.Errorf("housekeeping %q: exit %d, out %q, err %q", tc.args, code, out.String(), errb.String())
		}
	}
}

// TestOfflineLockCheck: with no bridge, bridge.lock naming a process that
// is gone is reported in a dry run and emptied otherwise, never removed.
func TestOfflineLockCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := offlineLockCheck(path, true, &out, &errb); code != 0 || !strings.Contains(out.String(), "would empty it") {
		t.Fatalf("dry run: %d %q %q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := offlineLockCheck(path, false, &out, &errb); code != 0 || !strings.Contains(out.String(), "emptied") {
		t.Fatalf("run: %d %q %q", code, out.String(), errb.String())
	}
	if data, err := os.ReadFile(path); err != nil || len(data) != 0 {
		t.Fatalf("bridge.lock after: %q, %v", data, err)
	}
	out.Reset()
	if code := offlineLockCheck(path, false, &out, &errb); code != 0 || !strings.Contains(out.String(), "nothing to do") {
		t.Fatalf("again: %d %q", code, out.String())
	}
}
