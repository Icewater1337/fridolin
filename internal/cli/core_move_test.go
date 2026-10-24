package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/Icewater1337/fridolin/internal/lease"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// The core commands a core move's agent runs (#187), on a folder store:
// export --move tags the export, status --json reads it back with the
// lease, release frees a lease this data dir holds.
func TestCoreMoveCommands(t *testing.T) {
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	storeDir, src := t.TempDir(), t.TempDir()
	makeFiles(t, src, "dashboard.token")
	cfg := writeConfig(t, `{"data_dir": "`+src+`"}`)
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		env := coreEnv{environ: []string{"FRIDOLIN_AGE_IDENTITY_PQ=" + id.String(), "FRIDOLIN_EXPORT_KEY=" + testLayerKey, "FRIDOLIN_HOME=" + src}, stdout: &stdout, stderr: &stderr}
		code := runCore(append(args, "--store", storeDir, "--config", cfg), env)
		return code, stdout.String(), stderr.String()
	}
	if code, _, _ := run("export", "--move", "m-bogus"); code != 2 {
		t.Errorf("export with a bad move id: %d", code)
	}
	move := "m-20261024-1200-beef"
	code, out, errOut := run("export", "--move", move, "--json")
	var info nodeproto.CoreExportInfo
	if code != 0 || json.Unmarshal([]byte(out), &info) != nil {
		t.Fatalf("export: %d %s %s", code, out, errOut)
	}
	dev, _ := lease.DeviceID(src)
	if info.Move != move || info.Device != dev || info.Bytes == 0 || !strings.HasPrefix(info.Key, "core/exports/") {
		t.Errorf("export info %+v", info)
	}
	if code, _, errOut := run("promote"); code != 0 {
		t.Fatalf("promote: %s", errOut)
	}
	status := func() nodeproto.CoreRuntime {
		t.Helper()
		code, out, errOut := run("status", "--json")
		var st nodeproto.CoreRuntime
		if code != 0 || json.Unmarshal([]byte(out), &st) != nil {
			t.Fatalf("status --json: %d %s %s", code, out, errOut)
		}
		return st
	}
	st := status()
	if st.Device != dev || !st.HeldBy() || st.Latest == nil || st.Latest.Move != move {
		t.Errorf("status: %+v lease %+v", st, st.Lease)
	}
	if code, out, _ := run("release"); code != 0 || !strings.Contains(out, "Released") {
		t.Errorf("release: %d %s", code, out)
	}
	if st := status(); st.Lease == nil || st.Lease.State != nodeproto.LeaseReleased || st.Lease.Device != dev {
		t.Errorf("after release: %+v", st.Lease)
	}
	if code, out, _ := run("release"); code != 0 || !strings.Contains(out, "nothing to release") {
		t.Errorf("a second release: %d %s", code, out)
	}
}

func TestCorePull(t *testing.T) {
	d, ran := hostFixture(t, mapSecrets{})
	image := "ghcr.io/you/fridolin-core@sha256:" + strings.Repeat("a", 64)
	var verified []string
	d.verify = func(_ context.Context, img, repo string) error {
		verified = append(verified, img+" "+repo)
		return nil
	}
	var stdout bytes.Buffer
	env := coreEnv{stdout: &stdout, stderr: io.Discard}
	if code := coreHost(context.Background(), "pull", []string{"--from", image, "--json"}, env, d); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var res nodeproto.CoreImageResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil || !res.Pulled || !res.Present {
		t.Fatalf("answer %s: %v", stdout.String(), err)
	}
	if !slices.Equal(verified, []string{image + " you/fridolin"}) {
		t.Errorf("verified %q", verified)
	}
	var cmds []string
	for _, c := range *ran {
		cmds = append(cmds, strings.Join(c.args, " "))
	}
	want := []string{"image inspect --format {{.Id}} fridolin-core:v1.4.0-dirty", "pull " + image, "tag " + image + " fridolin-core:v1.4.0-dirty"}
	if !slices.Equal(cmds, want) {
		t.Errorf("ran %q", cmds)
	}

	// Provenance that does not verify pulls nothing.
	*ran = nil
	d.verify = func(context.Context, string, string) error { return errors.New("no attestation") }
	if code := coreHost(context.Background(), "pull", []string{"--from", image}, coreEnv{stdout: io.Discard, stderr: io.Discard}, d); code != 1 || len(*ran) != 1 {
		t.Errorf("unverified: exit %d, ran %d", code, len(*ran))
	}
	// Not by digest, or not a fridolin-core image: refused.
	for _, bad := range []string{"ghcr.io/you/fridolin-core:latest", "docker.io/library/alpine@sha256:" + strings.Repeat("a", 64)} {
		if code := coreHost(context.Background(), "pull", []string{"--from", bad}, coreEnv{stdout: io.Discard, stderr: io.Discard}, d); code != 2 {
			t.Errorf("%s: exit %d", bad, code)
		}
	}
	// --check only looks.
	*ran = nil
	stdout.Reset()
	if code := coreHost(context.Background(), "pull", []string{"--check", "--json"}, env, d); code != 0 || len(*ran) != 1 || !strings.Contains(stdout.String(), `"present":true`) {
		t.Errorf("--check: exit %d, ran %v: %s", code, *ran, stdout.String())
	}
	// A host without gh, and without the image, cannot take a move: the
	// pre-flight's --check says so instead of the move failing at the pull.
	d.run = func(_ context.Context, _ string, args, _ []string, _, _ io.Writer) error {
		if len(args) > 1 && args[0] == "image" && args[1] == "inspect" {
			return errors.New("no such image")
		}
		return nil
	}
	d.noVerify = "GitHub's CLI (gh) is not installed"
	var errb bytes.Buffer
	if code := coreHost(context.Background(), "pull", []string{"--from", image, "--check", "--json"}, coreEnv{stdout: io.Discard, stderr: &errb}, d); code != 1 || !strings.Contains(errb.String(), "gh") {
		t.Errorf("--check without gh: exit %d: %s", code, errb.String())
	}
	// With the image there, gh is not needed.
	d.run = func(context.Context, string, []string, []string, io.Writer, io.Writer) error { return nil }
	if code := coreHost(context.Background(), "pull", []string{"--from", image, "--check"}, coreEnv{stdout: io.Discard, stderr: io.Discard}, d); code != 0 {
		t.Errorf("--check with the image, without gh: exit %d", code)
	}
}

// core rehearse imports into its scratch volume only, and removes it.
func TestCoreRehearse(t *testing.T) {
	d, ran := hostFixture(t, mapSecrets{})
	var stdout bytes.Buffer
	if code := coreHost(context.Background(), "rehearse", []string{"--json"}, coreEnv{stdout: &stdout, stderr: io.Discard}, d); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(*ran) != 3 {
		t.Fatalf("ran %v", *ran)
	}
	imp := (*ran)[1]
	if !slices.Contains(imp.env, "FRIDOLIN_VOLUME="+moveRehearsalVolume) || !slices.Equal(imp.args[len(imp.args)-4:], []string{"core", "import", "--latest", "--rehearsal"}) {
		t.Errorf("the import ran %q with %q", imp.args, imp.env)
	}
	for _, i := range []int{0, 2} {
		if got := strings.Join((*ran)[i].args, " "); got != "volume rm -f "+moveRehearsalVolume {
			t.Errorf("command %d: %s", i, got)
		}
	}
	if moveRehearsalVolume == coreVolume || filepath.Base(moveRehearsalVolume) != moveRehearsalVolume {
		t.Error("the scratch volume is the core's")
	}
	if !strings.Contains(stdout.String(), `"ok":true`) {
		t.Errorf("answer %s", stdout.String())
	}
}
