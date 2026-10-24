package secretxfer

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"filippo.io/age"
)

func TestSecretFilesTravelSealed(t *testing.T) {
	src := t.TempDir()
	for name, body := range map[string]string{
		"google_oauth_web_credentials.json":     `{"web":{}}`,
		"dashboard-users.json":                  `[]`,
		"google_calendar_token_personal.json":   `{"t":"x"}`,
		"google_calendar_credentials_team.json": `{"installed":{}}`,
		"restore.sh":                            "#!/bin/sh\n", // not a secret file
		"git-crypt-backup.key":                  "k",
	} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A link is never read, whatever its name.
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "google_calendar_token_evil.json")); err != nil {
		t.Fatal(err)
	}
	files, err := ReadFiles(src)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	want := []string{"dashboard-users.json", "google_calendar_credentials_team.json", "google_calendar_token_personal.json", "google_oauth_web_credentials.json"}
	if !slices.Equal(names, want) {
		t.Fatalf("read %q", names)
	}

	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	ct, err := SealBundleFiles("mini", map[string]string{"fridolin.export.key": "k1"}, files, "~/credentials", id.Recipient().String())
	if err != nil {
		t.Fatal(err)
	}
	secrets, got, dir, from, err := OpenBundleFiles(ct, id)
	if err != nil || from != "mini" || dir != "~/credentials" || secrets["fridolin.export.key"] != "k1" || string(got["dashboard-users.json"]) != "[]" {
		t.Fatalf("opened %v %v %q: %v", secrets, got, from, err)
	}

	// The target keeps what it has, writes the rest 0600.
	dst := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "dashboard-users.json"), []byte(`["mine"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	stored, kept, err := StoreFiles(dst, got)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(kept, []string{"dashboard-users.json"}) || len(stored) != 3 {
		t.Fatalf("stored %q kept %q", stored, kept)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "dashboard-users.json")); string(b) != `["mine"]` {
		t.Fatalf("a file there was replaced: %s", b)
	}
	if fi, err := os.Stat(filepath.Join(dst, "google_calendar_token_personal.json")); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("mode %v: %v", fi, err)
	}

	// Names a move does not carry are refused on both ends.
	if _, err := SealBundleFiles("mini", map[string]string{"fridolin.export.key": "k1"}, map[string][]byte{"../x.json": []byte("x")}, "~/credentials", id.Recipient().String()); err == nil {
		t.Fatal("sealed a file outside the names")
	}
	for _, bad := range []string{"/etc", "~/../etc", "~", "~/a/../../b", "credentials"} {
		if CheckFilesDir(bad) == nil {
			t.Errorf("the folder %q passed", bad)
		}
	}
	if _, _, err := StoreFiles(dst, map[string][]byte{"id_rsa": []byte("x")}); err == nil {
		t.Fatal("stored a file outside the names")
	}
}
