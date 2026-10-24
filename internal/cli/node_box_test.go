package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/container"
	"github.com/Icewater1337/fridolin/internal/tailnet"
)

func TestBoxJoinFlagsOfferNoCommandsOrNotifications(t *testing.T) {
	c, err := joinFlags{box: true}.capabilities("linux")
	if err != nil {
		t.Fatal(err)
	}
	if c.Exec != nil || c.Notify || !c.Host {
		t.Fatalf("a box's defaults: %+v", c)
	}
	// /backups is no folder on the test machine, so no backups by default.
	if c.Backup != nil {
		t.Fatalf("a box without /backups offers backups: %+v", c.Backup)
	}
	dir := t.TempDir()
	c, err = joinFlags{box: true, allow: multiFlag{"git=status"}, backupRoots: multiFlag{dir}}.capabilities("linux")
	if err != nil || c.Exec == nil || len(c.Exec.Allow) != 1 || c.Backup == nil || c.Backup.Roots[0] != dir {
		t.Fatalf("a box's flags: %+v, %v", c, err)
	}
}

func TestReadPairCode(t *testing.T) {
	for in, want := range map[string]string{
		"fp-abc123\n":                           "fp-abc123",
		`{"code":"fp-abc123","expires":"soon"}`: "fp-abc123",
	} {
		got, err := readPairCode(strings.NewReader(in))
		if err != nil || got != want {
			t.Errorf("readPairCode(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "two words", `{"nocode":1}`} {
		if _, err := readPairCode(strings.NewReader(in)); err == nil {
			t.Errorf("readPairCode(%q): no error", in)
		}
	}
}

func TestBoxComposeIsTheEmbeddedFile(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := boxCmd([]string{"compose"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !bytes.Equal(out.Bytes(), container.Box) {
		t.Fatal("box compose printed something else than container/box.yaml")
	}
	for _, want := range []string{"name: fridolin-box", "fridolin-node:/node", "fride-home:/var/lib/fride", "no-new-privileges:true"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the compose file lacks %q", want)
		}
	}
	if strings.Contains(out.String(), "docker.sock") {
		t.Error("the box's compose file mounts the Docker socket")
	}
	if code := boxCmd([]string{"nope"}, &out, &errOut); code != 2 {
		t.Errorf("an unknown box command: exit %d", code)
	}
}

func TestNodeTailnetUsesTheBoxDevice(t *testing.T) {
	fake := &tailnet.Fake{Status: tailnet.Status{DNSName: "fridolin-box-icebase.example.ts.net", IPs: []string{"100.64.0.9"}}}
	var got tailnet.Options
	old := newBoxTailnet
	newBoxTailnet = func(o tailnet.Options) (tailnet.Node, error) { got = o; return fake, nil }
	t.Cleanup(func() { newBoxTailnet = old })
	t.Setenv("TS_AUTHKEY", "tskey-test")
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := nodeCmd([]string{"tailnet", "--state-dir", dir, "--name", "IceBase"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if got.Hostname != "fridolin-box-icebase" || got.Dir != filepath.Join(dir, "tailnet") || got.AuthKey != "tskey-test" {
		t.Fatalf("the tailnet device: %+v", got)
	}
	if !strings.Contains(out.String(), "fridolin-box-icebase.example.ts.net") {
		t.Fatalf("output: %s", out.String())
	}
	if _, err := fake.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
}
