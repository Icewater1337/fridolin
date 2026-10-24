package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

const (
	testNodeImage  = "ghcr.io/owner/fridolin-core@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	testFrideImage = "ghcr.io/owner/fridolin-fride@sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// boxDir is a box's state dir with the core's key pinned and a request
// for nodeImage signed with it.
func boxDir(t *testing.T, nodeImage string) string {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, nodeagent.CoreKeyFile), []byte(hex.EncodeToString(key.Public().(ed25519.PublicKey))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := nodeproto.BoxUpdateParams{Version: "v1.1.0", NodeImage: nodeImage, FrideImage: testFrideImage}
	p.Signature = nodeproto.SignBoxUpdate(key, p.Version, p.NodeImage, p.FrideImage)
	data, _ := json.Marshal(nodeagent.BoxUpdateRequest{BoxUpdateParams: p, From: "v1.0.0", RequestedAt: time.Now()})
	if err := os.WriteFile(filepath.Join(dir, nodeagent.BoxUpdateFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testBoxDeps(verified *[]string, fail error) boxDeps {
	env := map[string]string{"FRIDOLIN_BOX_REGISTRY": "ghcr.io/owner", "FRIDOLIN_BOX_SOURCE": "https://raw.githubusercontent.com/Owner/fridolin/abc"}
	return boxDeps{
		getenv: func(k string) string { return env[k] },
		verify: func(_ context.Context, image, repo string) error {
			if repo != "Owner/fridolin" {
				return errors.New("repo " + repo)
			}
			*verified = append(*verified, image)
			return fail
		},
	}
}

func TestBoxPendingTakesASignedRequest(t *testing.T) {
	dir := boxDir(t, testNodeImage)
	var verified []string
	var out, errb bytes.Buffer
	if code := runBox([]string{"pending", "--state-dir", dir}, &out, &errb, testBoxDeps(&verified, nil)); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	want := "VERSION=v1.1.0\nNODE_IMAGE=" + testNodeImage + "\nFRIDE_IMAGE=" + testFrideImage + "\n"
	if out.String() != want {
		t.Fatalf("printed %q", out.String())
	}
	if len(verified) != 2 {
		t.Fatalf("provenance checked for %v", verified)
	}
	if _, err := os.Stat(filepath.Join(dir, nodeagent.BoxUpdateTakenFile)); err != nil {
		t.Fatal("the request is not marked taken")
	}
	// Taken: the next round finds nothing.
	out.Reset()
	if code := runBox([]string{"pending", "--state-dir", dir}, &out, &errb, testBoxDeps(&verified, nil)); code != 3 || out.Len() != 0 {
		t.Fatalf("a second round: exit %d, %q", code, out.String())
	}
	out.Reset()
	if code := runBox([]string{"result", "--state-dir", dir, "ok"}, &out, &errb, testBoxDeps(&verified, nil)); code != 0 {
		t.Fatalf("result: %d %s", code, errb.String())
	}
	var rep nodeproto.UpdateReport
	data, _ := os.ReadFile(filepath.Join(dir, nodeagent.BoxUpdateResultFile))
	if json.Unmarshal(data, &rep) != nil || !rep.OK || rep.From != "v1.0.0" || rep.To != "v1.1.0" {
		t.Fatalf("the result %s", data)
	}
}

func TestBoxPendingRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		image string
		fail  error
		want  string
	}{
		"another registry":  {image: "ghcr.io/evil/fridolin-core@sha256:" + strings.Repeat("1", 64), want: "is not ghcr.io/owner/fridolin-core"},
		"no provenance":     {image: testNodeImage, fail: errors.New("no attestations"), want: "no attestations"},
		"the fride as node": {image: "ghcr.io/owner/fridolin-fride@sha256:" + strings.Repeat("1", 64), want: "is not ghcr.io/owner/fridolin-core"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := boxDir(t, tc.image)
			var verified []string
			var out, errb bytes.Buffer
			if code := runBox([]string{"pending", "--state-dir", dir}, &out, &errb, testBoxDeps(&verified, tc.fail)); code != 1 || out.Len() != 0 {
				t.Fatalf("exit %d, printed %q", code, out.String())
			}
			if !strings.Contains(errb.String(), tc.want) {
				t.Fatalf("stderr %q", errb.String())
			}
			var rep nodeproto.UpdateReport
			data, _ := os.ReadFile(filepath.Join(dir, nodeagent.BoxUpdateResultFile))
			if json.Unmarshal(data, &rep) != nil || rep.OK || !strings.Contains(rep.Error, tc.want) {
				t.Fatalf("the agent reports %s", data)
			}
			if r, _ := nodeagent.ReadBoxUpdate(dir); r != nil {
				t.Fatal("a refused request stays pending")
			}
		})
	}
}

func TestBoxPendingRefusesAForgedSignature(t *testing.T) {
	dir := boxDir(t, testNodeImage)
	_, other, _ := ed25519.GenerateKey(nil)
	if err := os.WriteFile(filepath.Join(dir, nodeagent.CoreKeyFile), []byte(hex.EncodeToString(other.Public().(ed25519.PublicKey))), 0o600); err != nil {
		t.Fatal(err)
	}
	var verified []string
	var out, errb bytes.Buffer
	if code := runBox([]string{"pending", "--state-dir", dir}, &out, &errb, testBoxDeps(&verified, nil)); code != 1 || len(verified) != 0 {
		t.Fatalf("exit %d, verified %v", code, verified)
	}
}

func TestBoxUpdaterScripts(t *testing.T) {
	for _, kind := range []string{"sh", "ps1"} {
		var out, errb bytes.Buffer
		if code := runBox([]string{"updater", kind}, &out, &errb, boxDeps{}); code != 0 || !strings.Contains(out.String(), "box pending") {
			t.Fatalf("%s: exit %d", kind, code)
		}
		if strings.Contains(out.String(), "docker.sock") {
			t.Fatalf("the %s updater names the Docker socket", kind)
		}
	}
}

// box.sh installs the updater before docker compose made the node's
// volume; a round then must not docker run -v it into being, or compose
// never owns it and down -v leaves the node's key behind.
func TestBoxUpdaterWaitsForTheNodesVolume(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sh updater")
	}
	var out, errb bytes.Buffer
	if code := runBox([]string{"updater", "sh"}, &out, &errb, boxDeps{}); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	dir, bin := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{"update.sh": out.String(), ".env": "FRIDOLIN_BOX_NODE_IMAGE=" + testNodeImage + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	calls := filepath.Join(t.TempDir(), "calls")
	docker := "#!/bin/sh\necho \"$*\" >>" + calls + "\ncase \"$1\" in info) exit 0 ;; volume) exit 1 ;; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(docker), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", filepath.Join(dir, "update.sh"))
	cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	got, _ := os.ReadFile(calls)
	if strings.Contains(string(got), "run ") {
		t.Fatalf("the updater ran a container before the volume existed:\n%s", got)
	}
	if !strings.Contains(string(got), "volume inspect fridolin-node") {
		t.Fatalf("the updater did not look for the volume:\n%s", got)
	}
}
