package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeSbxScript is a stand-in for sbx: ls lists the sandbox fride-work,
// stopped; rm and stop work; everything else fails. The tests never run
// the machine's sbx.
const fakeSbxScript = `#!/bin/sh
echo "$@" >>"$(dirname "$0")/calls.log"
case "$1" in
ls) echo '[{"name":"fride-work","status":"stopped","agent":"claude"}]' ;;
rm|stop) ;;
*) echo "the fake runs no $1" >&2; exit 1 ;;
esac
`

// The commands' arguments, and what they refuse before they run sbx.
func TestNodeFrideHostArgs(t *testing.T) {
	t.Parallel()
	dir, _ := joinedState(t, "mini")
	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := nodeCmd(append([]string{"fride"}, args...), &out, &errOut)
		return code, out.String(), errOut.String()
	}
	if code, out, _ := run(); code != 0 || !strings.Contains(out, "fride host setup|status") || strings.Contains(out, "sandbox join") {
		t.Errorf("fride help: %d %q", code, out)
	}
	if code, out, _ := run("host"); code != 0 || !strings.Contains(out, "Docker Sandbox") || !strings.Contains(out, "--reserve-mb") {
		t.Errorf("host help: %d %q", code, out)
	}
	if code, out, _ := run("instance", "help"); code != 0 || !strings.Contains(out, "instance rm NAME") {
		t.Errorf("instance help: %d %q", code, out)
	}
	for _, args := range [][]string{
		{"sandbox", "create"},
		{"host", "sideways"},
		{"host", "status", "extra", "--state-dir", dir},
		{"instance", "stop", "--state-dir", dir},
		{"instance", "stop", "Work", "--state-dir", dir},
		{"instance", "stop", "native", "--state-dir", dir},
		{"instance", "logs", "work", "--lines", "-1", "--state-dir", dir},
		{"instance", "ls", "work", "--state-dir", dir},
	} {
		if code, _, _ := run(args...); code != 2 {
			t.Errorf("%q: %d", args, code)
		}
	}
	var out, errOut bytes.Buffer
	if code := runFrideHost("instance ls", nil, frideHostFlags{}, dir, "", &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "sbx is not installed") {
		t.Errorf("ls without sbx: %d %q", code, errOut.String())
	}
	out.Reset()
	if code := runFrideHost("host status", nil, frideHostFlags{}, dir, "", &out, &errOut); code != 1 || !strings.Contains(out.String(), "not set up") {
		t.Errorf("status before setup: %d %q", code, out.String())
	}
}

// ls, stop, logs and rm on an instance the node has, with a fake sbx.
func TestNodeFrideInstance(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Docker Sandboxes run on macOS and Linux")
	}
	dir, _ := joinedState(t, "mini")
	bin := t.TempDir()
	sbx := filepath.Join(bin, "sbx")
	if err := os.WriteFile(sbx, []byte(fakeSbxScript), 0o755); err != nil {
		t.Fatal(err)
	}
	inst := filepath.Join(dir, "fride-instances", "work")
	if err := os.MkdirAll(inst, 0o700); err != nil {
		t.Fatal(err)
	}
	state := `{"name":"work","sandbox":"fride-work","memory_mb":4096,"template":"t","port":17850,"webhook_port":17851,"run":true}`
	if err := os.WriteFile(filepath.Join(inst, "instance.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	call := func(cmd string, pos []string, f frideHostFlags) (int, string, string) {
		var out, errOut bytes.Buffer
		code := runFrideHost(cmd, pos, f, dir, sbx, &out, &errOut)
		return code, out.String(), errOut.String()
	}
	if code, out, e := call("instance ls", nil, frideHostFlags{}); code != 0 || !strings.Contains(out, "work: starting, 4096 MB, sandbox fride-work (stopped), ports 17850 and 17851") {
		t.Errorf("ls: %d %q %q", code, out, e)
	}
	if code, _, e := call("instance logs", []string{"work"}, frideHostFlags{}); code != 1 || !strings.Contains(e, "is stopped") {
		t.Errorf("logs of a stopped sandbox: %d %q", code, e)
	}
	if code, out, e := call("instance stop", []string{"work"}, frideHostFlags{}); code != 0 || !strings.Contains(out, "Stopped") {
		t.Errorf("stop: %d %q %q", code, out, e)
	}
	if code, out, _ := call("instance ls", nil, frideHostFlags{}); code != 0 || !strings.Contains(out, "work: stopped") {
		t.Errorf("ls after stop: %q", out)
	}
	if code, _, e := call("instance stop", []string{"home"}, frideHostFlags{}); code != 1 || !strings.Contains(e, "instance ls") {
		t.Errorf("stop of an unknown instance: %d %q", code, e)
	}
	if code, _, e := call("instance rm", []string{"work"}, frideHostFlags{}); code != 1 || !strings.Contains(e, "--yes") {
		t.Errorf("rm without --yes and no terminal: %d %q", code, e)
	}
	if code, out, e := call("instance rm", []string{"work"}, frideHostFlags{yes: true, noHistory: true}); code != 0 || !strings.Contains(out, "Removed") {
		t.Errorf("rm: %d %q %q", code, out, e)
	}
	if _, err := os.Stat(filepath.Join(inst, "instance.json")); err == nil {
		t.Error("rm left the state")
	}
	calls, _ := os.ReadFile(filepath.Join(bin, "calls.log"))
	if !strings.Contains(string(calls), "rm -f fride-work") {
		t.Errorf("sbx calls: %s", calls)
	}
	if code, out, _ := call("instance ls", nil, frideHostFlags{}); code != 0 || !strings.Contains(out, "No FrIDE instances") {
		t.Errorf("ls after rm: %q", out)
	}
}

// fakeSetupSbx is a stand-in for sbx that answers what host setup asks
// of a node set up before: running, signed in, fridolin's settings.
const fakeSetupSbx = `#!/bin/sh
echo "$@" >>"$(dirname "$0")/calls.log"
case "$1" in
version) echo "sbx version v0.50.0" ;;
diagnose) echo '{"version":"1.0","checks":[{"name":"Daemon","status":"pass"},{"name":"Authentication","status":"pass"}]}' ;;
ls) echo '[]' ;;
settings) echo '[{"key":"browser.enabled","value":false},{"key":"ssh.agentForwardingEnabled","value":false}]' ;;
policy) echo '{"rules":[{"id":"d","scope":"global","resource_type":"network","decision":"deny","resources":["**"],"status":"active"}]}' ;;
*) echo "the fake runs no $1" >&2; exit 1 ;;
esac
`

// Without Docker, setup takes the core's template (--no-build) and
// needs no checkout; a checkout named without Docker, or with
// --no-build, is refused; on Windows it keeps no shared caches.
func TestNodeFrideHostSetupWithoutDocker(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("the fake sbx is a shell script")
	}
	dir, _ := joinedState(t, "mini")
	sbx := filepath.Join(t.TempDir(), "sbx")
	if err := os.WriteFile(sbx, []byte(fakeSetupSbx), 0o755); err != nil {
		t.Fatal(err)
	}
	fail := func(errOut *bytes.Buffer) func(string, ...any) int {
		return func(format string, args ...any) int { fmt.Fprintf(errOut, format, args...); return 1 }
	}
	var out, errOut bytes.Buffer
	f := frideHostFlags{noCaches: true, set: map[string]bool{}}
	if code := frideHostSetup(context.Background(), f, dir, sbx, "", "linux", &out, fail(&errOut)); code != 0 {
		t.Fatalf("setup: %d %s %s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "No Docker here: taking the template the core publishes (--no-build).") ||
		!strings.Contains(out.String(), "the template the core publishes (fridolin-fride-sandbox") {
		t.Errorf("setup said:\n%s", out.String())
	}
	b, _ := os.ReadFile(filepath.Join(dir, "fride-host.json"))
	if !strings.Contains(string(b), `"template_from_core": true`) {
		t.Errorf("fride-host.json: %s", b)
	}
	out.Reset()
	if code := runFrideHost("host status", nil, frideHostFlags{}, dir, sbx, &out, &errOut); code != 0 || !strings.Contains(out.String(), "Template: the core's (fridolin-fride-sandbox") || !strings.Contains(out.String(), "Ready: yes") {
		t.Errorf("status: %d %s %s", code, out.String(), errOut.String())
	}

	errOut.Reset()
	f.set["repo"] = true
	if code := frideHostSetup(context.Background(), f, dir, sbx, "", "linux", &out, fail(&errOut)); code != 1 || !strings.Contains(errOut.String(), "--no-build") {
		t.Errorf("a checkout without Docker: %d %q", code, errOut.String())
	}
	out.Reset()
	f = frideHostFlags{noBuild: true, set: map[string]bool{}}
	if code := frideHostSetup(context.Background(), f, dir, sbx, "", "windows", &out, fail(&errOut)); code != 0 || !strings.Contains(out.String(), "no shared Go and npm caches on Windows") {
		t.Errorf("on Windows: %d %s %s", code, out.String(), errOut.String())
	}
	var code int
	code = nodeCmd([]string{"fride", "host", "setup", "--no-build", "--repo", ".", "--state-dir", dir}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "--no-build takes the core's") {
		t.Errorf("--no-build with --repo: %d %q", code, errOut.String())
	}
}

// sbx-secret prints an instance's token file for sbx, and nothing else.
func TestNodeFrideHostSbxSecret(t *testing.T) {
	t.Parallel()
	tokens := filepath.Join(t.TempDir(), "fride-instances", "work", "tokens")
	if err := os.MkdirAll(tokens, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(tokens, "github.com.token")
	if err := os.WriteFile(file, []byte("ghs_madeUp"), 0o600); err != nil { // personal-data:ignore (a made-up token)
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := nodeCmd([]string{"fride", "host", "sbx-secret", file}, &out, &errOut); code != 0 || out.String() != "ghs_madeUp" {
		t.Errorf("%d %q %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := nodeCmd([]string{"fride", "host", "sbx-secret", filepath.Join(tokens, "..", "instance.json")}, &out, &errOut); code != 1 || out.Len() != 0 {
		t.Errorf("another file: %d %q", code, out.String())
	}
	if code := nodeCmd([]string{"fride", "host", "sbx-secret"}, &out, &errOut); code != 2 {
		t.Errorf("no file: %d", code)
	}
}
