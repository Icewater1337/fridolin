package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppArmorOverride(t *testing.T) {
	dir := t.TempDir()
	compose := filepath.Join(dir, "compose.yaml")
	var warned []string
	warn := func(s string) { warned = append(warned, s) }
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	p, err := apparmorOverride("linux", compose, apparmorState{Enabled: true, Restricted: true, Loaded: true}, warn)
	if err != nil || !strings.Contains(read(p), "- apparmor=fridolin-core\n") || len(warned) != 0 {
		t.Fatalf("loaded: %q %v %v", p, err, warned)
	}
	// Not loaded, and Ubuntu restricts user namespaces: unconfined, which
	// does not do, with a warning that says what does.
	p, err = apparmorOverride("linux", compose, apparmorState{Enabled: true, Restricted: true}, warn)
	if err != nil || !strings.Contains(read(p), "- apparmor=unconfined\n") || len(warned) != 1 || !strings.Contains(warned[0], "core apparmor") {
		t.Fatalf("restricted, not loaded: %q %v %v", p, err, warned)
	}
	// AppArmor without the restriction (Debian): unconfined is enough.
	warned = nil
	if _, err := apparmorOverride("linux", compose, apparmorState{Enabled: true}, warn); err != nil || len(warned) != 0 {
		t.Fatalf("unrestricted: %v %v", err, warned)
	}
	// No AppArmor, or not Linux: no override, and a stale one goes.
	for _, goos := range []string{"linux", "darwin", "windows"} {
		st := apparmorState{Enabled: goos != "linux"}
		if p, err := apparmorOverride(goos, compose, st, warn); p != "" || err != nil {
			t.Errorf("%s: override %q, %v", goos, p, err)
		}
		if _, err := os.Stat(filepath.Join(dir, apparmorOverrideName)); err == nil {
			t.Errorf("%s: a stale override stayed", goos)
		}
	}
}

func TestCoreAppArmorInstalls(t *testing.T) {
	dir := t.TempDir()
	var ran []string
	d := hostDeps{goos: "linux", uid: 0, apparmorDir: dir, apparmor: func() apparmorState { return apparmorState{Enabled: true, Restricted: true} },
		run: func(_ context.Context, name string, args, _ []string, _, _ io.Writer) error {
			ran = append(ran, name+" "+strings.Join(args, " "))
			return nil
		}}
	var out, errb bytes.Buffer
	env := coreEnv{stdout: &out, stderr: &errb}
	if code := coreAppArmor(context.Background(), nil, env, d); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	b, err := os.ReadFile(filepath.Join(dir, "fridolin-core"))
	if err != nil || !strings.Contains(string(b), "profile fridolin-core flags=(unconfined) {\n  userns,\n}") {
		t.Errorf("profile = %q, %v", b, err)
	}
	if len(ran) != 1 || ran[0] != "apparmor_parser -r "+filepath.Join(dir, "fridolin-core") {
		t.Errorf("ran %v", ran)
	}

	d.uid = 1000
	errb.Reset()
	if code := coreAppArmor(context.Background(), nil, env, d); code != 1 || !strings.Contains(errb.String(), "sudo") {
		t.Errorf("as a user: exit %d, %q", code, errb.String())
	}
	d.goos = "darwin"
	if code := coreAppArmor(context.Background(), nil, env, d); code != 2 {
		t.Errorf("on macOS: exit %d", code)
	}
}
