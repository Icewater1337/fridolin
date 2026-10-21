package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

func TestParseAllow(t *testing.T) {
	for in, want := range map[string]string{
		"go":                   `{"name":"go","args":null}`,
		"hostname=":            `{"name":"hostname","args":[]}`,
		"git=status,log":       `{"name":"git","args":["status","log"]}`,
		`C:\Tools\x.exe=-v`:    `{"name":"C:\\Tools\\x.exe","args":["-v"]}`,
		"/usr/bin/uname=-[a]+": `{"name":"/usr/bin/uname","args":["-[a]+"]}`,
	} {
		got, _ := json.Marshal(parseAllow(in))
		if string(got) != want {
			t.Errorf("parseAllow(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestJoinFlagsCapabilities(t *testing.T) {
	dir := t.TempDir()
	c, err := joinFlags{}.capabilities("linux")
	if err != nil || c.Exec == nil || len(c.Exec.Allow) != len(nodeproto.DefaultExecAllow("linux")) || !c.Host || !c.Notify || c.FS != nil {
		t.Fatalf("the defaults: %+v, %v", c, err)
	}
	c, err = joinFlags{allow: multiFlag{"git=status"}, roots: multiFlag{dir + ":rw", dir}, noNotify: true}.capabilities("linux")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Exec.Allow) != 1 || c.Exec.Allow[0].Name != "git" || c.Notify || len(c.FS.Roots) != 2 || !c.FS.Roots[0].Write || c.FS.Roots[1].Write {
		t.Fatalf("%+v", c)
	}
	if c, err := (joinFlags{noExec: true, noHost: true}).capabilities("windows"); err != nil || c.Exec != nil || c.Host {
		t.Fatalf("--no-exec --no-host: %+v, %v", c, err)
	}
	for name, o := range map[string]joinFlags{
		"a shell":        {allow: multiFlag{"bash"}},
		"pwsh by path":   {allow: multiFlag{`C:\Program Files\PowerShell\7\pwsh.exe`}},
		"missing root":   {roots: multiFlag{filepath.Join(dir, "nope")}},
		"a bad pattern":  {allow: multiFlag{"git=("}},
		"relative names": {allow: multiFlag{"bin/tool"}},
	} {
		if _, err := o.capabilities("linux"); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestNodeCommandsRefuseBadArguments(t *testing.T) {
	for _, c := range []struct {
		args []string
		cmd  func([]string, *bytes.Buffer, *bytes.Buffer) int
		want int
		says string
	}{
		{[]string{"help"}, nodeRun, 0, "fridolin node join CODE"},
		{nil, nodeRun, 0, "fridolin node join CODE"},
		{[]string{"join", "--state-dir", t.TempDir()}, nodeRun, 2, "takes the join code"},
		{[]string{"join", "fj-x", "--state-dir", t.TempDir()}, nodeRun, 2, "--core HOST:PORT is missing"},
		{[]string{"join", "fj-x", "--core", "c:1", "--allow", "sh", "--state-dir", t.TempDir()}, nodeRun, 2, "shell"},
		{[]string{"frobnicate", "--state-dir", t.TempDir()}, nodeRun, 2, "unknown node command"},
		{[]string{"run", "extra", "--state-dir", t.TempDir()}, nodeRun, 2, "takes no arguments"},
		{[]string{"help"}, nodesRun, 0, "fridolin nodes join-code"},
		{[]string{"approve"}, nodesRun, 2, "wrong number of arguments"},
		{[]string{"revoke", "a", "b"}, nodesRun, 2, "wrong number of arguments"},
		{[]string{"list", "--commands"}, nodesRun, 2, "--commands goes with approve"},
		{[]string{"frobnicate"}, nodesRun, 2, "unknown nodes command"},
	} {
		var out, errOut bytes.Buffer
		if got := c.cmd(c.args, &out, &errOut); got != c.want {
			t.Errorf("%q: exit %d, want %d (%s%s)", c.args, got, c.want, out.String(), errOut.String())
		}
		if !strings.Contains(out.String()+errOut.String(), c.says) {
			t.Errorf("%q: says %q%q, want %q", c.args, out.String(), errOut.String(), c.says)
		}
	}
}

func nodeRun(args []string, out, errOut *bytes.Buffer) int  { return nodeCmd(args, out, errOut) }
func nodesRun(args []string, out, errOut *bytes.Buffer) int { return nodesCmd(args, out, errOut) }

func TestStatusOfANodeThatHasNotJoined(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	// status asks the service manager too, which says the service is not
	// installed (or that it cannot tell); either way the command answers.
	nodeCmd([]string{"status", "--state-dir", dir}, &out, &errOut)
	if !strings.Contains(out.String(), "State dir: "+dir) || !strings.Contains(out.String(), "Not joined") {
		t.Errorf("status: %s %s", out.String(), errOut.String())
	}
}

func TestWorkstationFlags(t *testing.T) {
	dir := t.TempDir()
	c, err := joinFlags{workstation: true, roots: multiFlag{dir + ":rw"}}.capabilities("linux")
	if err != nil || c.Agent == nil || c.Terminal == nil || len(c.Agent.Roots) != 1 || c.Agent.Roots[0] != dir {
		t.Fatalf("--workstation --root: %+v, %v", c, err)
	}
	other := t.TempDir()
	c, err = joinFlags{workRoots: multiFlag{other}, roots: multiFlag{dir}}.capabilities("linux")
	if err != nil || c.Agent.Roots[0] != other {
		t.Fatalf("--workstation-root: %+v, %v", c.Agent, err)
	}
	if _, err := (joinFlags{workstation: true}).capabilities("linux"); err == nil {
		t.Error("--workstation without a folder")
	}
	if _, err := (joinFlags{workRoots: multiFlag{"/"}}).capabilities("linux"); err == nil {
		t.Error("a whole drive as a workstation root")
	}

	state := t.TempDir()
	cfg := `{"core": "c:1", "name": "n", "capabilities": {"host": true}}`
	if err := os.WriteFile(filepath.Join(state, "node.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := nodeCmd([]string{"workstation", "on", "--root", dir, "--no-terminal", "--state-dir", state}, &out, &errOut); code != 0 {
		t.Fatalf("workstation on: %d %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "agent (1 folder)") || strings.Contains(out.String(), "terminal") {
		t.Fatalf("workstation on says %q", out.String())
	}
	out.Reset()
	if code := nodeCmd([]string{"workstation", "off", "--state-dir", state}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "offers: host\n") {
		t.Fatalf("workstation off: %d %q", code, out.String())
	}
}
