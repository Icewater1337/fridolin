package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
)

const testFamily = "Claude_pzs8sxrjxfjjc"

// packagedFixture is an AppData in a temp dir and two views of it: inside,
// a process of the Claude desktop app's package, whose writes Windows
// keeps in the package's copy and which sees that copy in place of the
// real folder; outside, any other process, which sees the real folder
// (platform.AppData itself, in a temp dir that no package redirects).
func packagedFixture(t *testing.T) (inside, outside appPackage, a platform.AppData) {
	t.Helper()
	root := t.TempDir()
	a = platform.AppData{Local: filepath.Join(root, "AppData", "Local"), Roaming: filepath.Join(root, "AppData", "Roaming")}
	if err := os.MkdirAll(a.Local, 0o700); err != nil {
		t.Fatal(err)
	}
	inside = appPackage{appData: a, home: root,
		redirected: func(dir string) (string, string, bool) {
			c, ok := a.PackageCopy(testFamily, dir)
			return c, testFamily, ok
		},
		seenAsCopy: func(path string) (string, string, bool) {
			c, ok := a.PackageCopy(testFamily, path)
			if _, err := os.Stat(c); !ok || err != nil {
				return "", "", false
			}
			return c, testFamily, true
		}}
	outside = appPackage{appData: a, home: root, redirected: a.WritesRedirected, seenAsCopy: a.SeenAsCopy}
	return inside, outside, a
}

// joinedIn writes a joined node's node.json and node.key into dir.
func joinedIn(t *testing.T, dir string) {
	t.Helper()
	if _, err := nodeagent.NewKey(dir); err != nil {
		t.Fatal(err)
	}
	if err := nodeagent.SaveConfig(dir, nodeagent.Config{Core: "fridolin-core.example.ts.net:8766", Name: "winbox", Capabilities: nodeproto.Capabilities{Host: true}}); err != nil {
		t.Fatal(err)
	}
}

func TestWriteErrorInsideAnAppPackage(t *testing.T) {
	inside, outside, a := packagedFixture(t)
	dir := filepath.Join(a.Local, "fridolin-node")
	err := inside.writeError(dir)
	copyDir, _ := a.PackageCopy(testFamily, dir)
	for _, want := range []string{"this process runs inside the app package " + testFamily, copyDir, "1053", "outside the app", `--state-dir "` + inside.outsideDir() + `"`, packageDoc} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("redirected: %v, want %q in it", err, want)
		}
	}
	if err := inside.writeError(inside.outsideDir()); err != nil {
		t.Errorf("outside AppData: %v", err)
	}
	if err := outside.writeError(dir); err != nil {
		t.Errorf("outside the package: %v", err)
	}
	if err := (appPackage{}).writeError(dir); err != nil {
		t.Errorf("not Windows: %v", err)
	}
}

func TestServiceErrorInsideAnAppPackage(t *testing.T) {
	inside, outside, a := packagedFixture(t)
	dir := filepath.Join(a.Local, "fridolin-node")
	// Joined from a PowerShell outside the app: the package sees the real
	// node.json, and so does the service.
	joinedIn(t, dir)
	if err := inside.serviceError(dir); err != nil {
		t.Errorf("the real node.json: %v", err)
	}
	// Joined from inside: node.json is the package's copy.
	copyDir, _ := a.PackageCopy(testFamily, dir)
	joinedIn(t, copyDir)
	err := inside.serviceError(dir)
	for _, want := range []string{testFamily, filepath.Join(copyDir, nodeagent.ConfigFile), "1053", nodeagent.KeyFile, inside.outsideDir()} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("the package's copy: %v, want %q in it", err, want)
		}
	}
	if err := outside.serviceError(dir); err != nil {
		t.Errorf("outside the package: %v", err)
	}
}

func TestServiceDirProblem(t *testing.T) {
	inside, outside, a := packagedFixture(t)
	sd := filepath.Join(a.Local, "fridolin-node")
	if detail, _ := outside.serviceDirProblem(""); detail != "" {
		t.Errorf("no state dir known: %s", detail)
	}
	detail, fix := outside.serviceDirProblem(sd)
	if !strings.Contains(detail, "has no node.json") || !strings.Contains(detail, "1053") || !strings.Contains(fix, "fridolin node install --state-dir") {
		t.Errorf("an empty state dir: %s / %s", detail, fix)
	}
	// What the agent wrote from inside the Claude desktop app.
	copyDir, _ := a.PackageCopy(testFamily, sd)
	joinedIn(t, copyDir)
	detail, fix = outside.serviceDirProblem(sd)
	if !strings.Contains(detail, "has no node.json") || !strings.Contains(detail, copyDir) || !strings.Contains(fix, sd) || !strings.Contains(fix, packageDoc) {
		t.Errorf("the files in the package's copy, seen from outside: %s / %s", detail, fix)
	}
	detail, fix = inside.serviceDirProblem(sd)
	if !strings.Contains(detail, "own copy") || !strings.Contains(detail, copyDir) || !strings.Contains(fix, "outside the app") {
		t.Errorf("the files in the package's copy, seen from inside: %s / %s", detail, fix)
	}
	joinedIn(t, sd)
	if detail, _ := outside.serviceDirProblem(sd); detail != "" {
		t.Errorf("node.json where the service looks: %s", detail)
	}
}

func TestNodeChecksSeeTheServiceStateDir(t *testing.T) {
	inside, outside, a := packagedFixture(t)
	dir := filepath.Join(a.Local, "fridolin-node")
	copyDir, _ := a.PackageCopy(testFamily, dir)
	joinedIn(t, copyDir)
	stopped := func() (string, error) { return "installed, stopped", nil }
	running := func() (string, error) { return "running", nil }
	registered := func() (string, error) { return dir, nil }
	env := doctorEnvFor("fridolin-core.example.ts.net:8766", nil)
	check := func(rep app.DoctorReport, id string) app.DoctorCheckReport {
		t.Helper()
		for _, c := range rep.Checks {
			if c.ID == id {
				return c
			}
		}
		t.Fatalf("no %s check: %+v", id, rep)
		return app.DoctorCheckReport{}
	}
	// From a normal PowerShell: not joined here, but the node's files are in
	// the package's copy.
	rep := app.RunDoctor(context.Background(), env, nodeChecks(dir, nodeService{status: stopped, stateDir: registered, pkg: outside}))
	if c := check(rep, "node-joined"); c.Status != app.DoctorFail || !strings.Contains(c.Detail, copyDir) || !strings.Contains(c.Fix, packageDoc) {
		t.Errorf("node-joined: %+v", c)
	}
	// From inside the app, which sees its copy in dir (here, a second copy
	// stands in for that view): joined, and the service stopped because it
	// does not see it.
	joinedIn(t, dir)
	rep = app.RunDoctor(context.Background(), env, nodeChecks(dir, nodeService{status: stopped, stateDir: registered, pkg: inside}))
	if c := check(rep, "node-joined"); c.Status != app.DoctorOK {
		t.Errorf("node-joined inside the package: %+v", c)
	}
	if c := check(rep, "node-service"); c.Status != app.DoctorFail || !strings.Contains(c.Detail, "1053") || !strings.Contains(c.Detail, copyDir) {
		t.Errorf("node-service inside the package: %+v", c)
	}
	var out bytes.Buffer
	if code := writeMachineReport(&out, rep, dir, false); code != 1 || !strings.Contains(out.String(), "Next: check "+dir+" from a PowerShell started outside the app") {
		t.Errorf("next step: %d %s", code, out.String())
	}
	// A service that runs says which state dir it has, if another.
	other := inside.outsideDir()
	rep = app.RunDoctor(context.Background(), env, nodeChecks(dir, nodeService{status: running, stateDir: func() (string, error) { return other, nil }, pkg: inside}))
	if c := check(rep, "node-service"); c.Status != app.DoctorOK || !strings.Contains(c.Detail, "with --state-dir "+other) {
		t.Errorf("node-service running elsewhere: %+v", c)
	}
}

// Inside an app package that redirects AppData, join stops before it
// spends the code or writes a key.
func TestJoinInsideAnAppPackage(t *testing.T) {
	inside, _, a := packagedFixture(t)
	dir := filepath.Join(a.Local, "fridolin-node")
	var out, errOut bytes.Buffer
	if code := nodeJoin("fj-x", dir, joinFlags{core: "127.0.0.1:1", noExec: true, noSvc: true}, inside, &out, &errOut); code != 1 {
		t.Fatalf("exit %d: %s%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "fridolin node join: this process runs inside the app package "+testFamily) {
		t.Errorf("says %q", errOut.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("wrote the state dir: %v", err)
	}
}

// The other commands that rewrite node.json refuse inside the package too.
func TestEditsInsideAnAppPackage(t *testing.T) {
	inside, _, a := packagedFixture(t)
	dir := filepath.Join(a.Local, "fridolin-node")
	joinedIn(t, dir)
	before, _ := os.ReadFile(filepath.Join(dir, nodeagent.ConfigFile))
	for name, run := range map[string]func(out, errOut *bytes.Buffer) int{
		"backup":      func(out, errOut *bytes.Buffer) int { return nodeBackup([]string{"on"}, nil, dir, inside, out, errOut) },
		"workstation": func(out, errOut *bytes.Buffer) int { return nodeWorkstation([]string{"off"}, workstationFlags{}, dir, inside, out, errOut) },
	} {
		var out, errOut bytes.Buffer
		if code := run(&out, &errOut); code != 1 || !strings.Contains(errOut.String(), "fridolin node "+name+": this process runs inside the app package") {
			t.Errorf("%s: exit %d: %s%s", name, code, out.String(), errOut.String())
		}
	}
	if after, _ := os.ReadFile(filepath.Join(dir, nodeagent.ConfigFile)); !bytes.Equal(before, after) {
		t.Error("node.json changed")
	}
}
