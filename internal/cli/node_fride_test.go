package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// `fridolin node fride pair` asks fride pair-companion for the code (here
// a stand-in script printing the fake FrIDE's), pairs, and status then
// says FrIDE answers.
func TestNodeFridePairAndStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in fride is a shell script")
	}
	ff := testutil.StartFakeFride(t, "api")
	dir := t.TempDir()
	if err := nodeagent.SaveConfig(dir, nodeagent.Config{Core: "core:8766", Name: "laptop", Capabilities: nodeproto.Capabilities{Host: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeagent.NewKey(dir); err != nil {
		t.Fatal(err)
	}
	fride := filepath.Join(t.TempDir(), "fride")
	script := "#!/bin/sh\n[ \"$1 $2\" = \"pair-companion --json\" ] || exit 2\n[ \"$FRIDE_HOME\" = /tmp/fride-home ] || exit 3\necho '{\"code\":\"" + ff.NewCode() + "\",\"expires\":\"later\"}'\n"
	if err := os.WriteFile(fride, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := nodeCmd([]string{"fride", "pair", "--state-dir", dir, "--url", ff.URL, "--fride", fride, "--fride-home", "/tmp/fride-home"}, &out, &errOut); code != 0 {
		t.Fatalf("pair: %d\n%s%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "Paired with FrIDE at "+ff.URL+" (repositories: api).") || !strings.Contains(out.String(), "fridolin nodes approve ") {
		t.Errorf("pair said:\n%s", out.String())
	}
	out.Reset()
	if code := nodeCmd([]string{"fride", "status", "--state-dir", dir}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "Answers: yes, companion mode, repositories api") {
		t.Errorf("status: %d\n%s%s", code, out.String(), errOut.String())
	}
	ff.Stop()
	out.Reset()
	if code := nodeCmd([]string{"fride", "status", "--state-dir", dir}, &out, &errOut); code != 1 || !strings.Contains(out.String(), "Answers: no") {
		t.Errorf("status with FrIDE stopped: %d\n%s", code, out.String())
	}
	for _, args := range [][]string{{"fride", "pair", "--state-dir", dir}, {"fride", "nope", "--state-dir", dir}, {"fride", "status", "extra", "--state-dir", dir}} {
		if code := nodeCmd(args, &out, &errOut); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
	}
}
