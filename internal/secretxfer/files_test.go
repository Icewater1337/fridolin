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
	for _, bad := range []string{"/etc", "~/../etc", "~", "~/a/../../b", "credentials", "~/.", "~/x/..", "~/a b"} {
		if CheckFilesDir(bad) == nil {
			t.Errorf("the folder %q passed", bad)
		}
	}
	// A hidden folder, as a credentials folder usually is.
	for _, good := range []string{"~/.creds", "~/.config/creds", "~/creds"} {
		if err := CheckFilesDir(good); err != nil {
			t.Errorf("the folder %q: %v", good, err)
		}
	}
	if _, _, err := StoreFiles(dst, map[string][]byte{"id_rsa": []byte("x")}); err == nil {
		t.Fatal("stored a file outside the names")
	}
}

// The local files (the data dir's core/local-secrets: the GitLab
// connection) travel sealed too, with or without a credentials folder,
// and replace what the target has.
func TestLocalFilesTravelSealed(t *testing.T) {
	src := t.TempDir()
	dir := filepath.Join(src, filepath.FromSlash(LocalDir))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"gitlab.json": `{"token":"new"}`, "github.json": `{"name":"Jane"}`, "notes.txt": "not a secret file"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := ReadLocalFiles(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || string(files["local:gitlab.json"]) != `{"token":"new"}` {
		t.Fatalf("read %q", files)
	}
	if none, err := ReadLocalFiles(t.TempDir()); err != nil || len(none) != 0 {
		t.Errorf("no folder: %v %v", none, err)
	}
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	// No credentials folder is needed for local files alone.
	ct, err := SealBundleFiles("mini", map[string]string{"fridolin.export.key": "k1"}, files, "", id.Recipient().String())
	if err != nil {
		t.Fatal(err)
	}
	_, got, filesDir, _, err := OpenBundleFiles(ct, id)
	if err != nil || filesDir != "" || len(got) != 2 {
		t.Fatalf("opened %q %q: %v", got, filesDir, err)
	}
	if _, _, err := StoreFiles(t.TempDir(), got); err == nil {
		t.Error("StoreFiles wrote local files into a credentials folder")
	}
	creds, local := SplitFiles(got)
	if len(creds) != 0 || len(local) != 2 {
		t.Errorf("split %q %q", creds, local)
	}

	dst := t.TempDir()
	ddir := filepath.Join(dst, filepath.FromSlash(LocalDir))
	if err := os.MkdirAll(ddir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ddir, "gitlab.json"), []byte(`{"token":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stored, replaced, err := StoreLocalFiles(dst, local)
	if err != nil || !slices.Equal(stored, []string{"local:github.json"}) || !slices.Equal(replaced, []string{"local:gitlab.json"}) {
		t.Fatalf("stored %q replaced %q: %v", stored, replaced, err)
	}
	if b, _ := os.ReadFile(filepath.Join(ddir, "gitlab.json")); string(b) != `{"token":"new"}` {
		t.Errorf("the old connection stays: %s", b)
	}
	if fi, err := os.Stat(filepath.Join(ddir, "github.json")); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Errorf("mode %v: %v", fi, err)
	}
	// The same again changes nothing.
	if stored, replaced, err := StoreLocalFiles(dst, local); err != nil || len(stored)+len(replaced) != 0 {
		t.Errorf("again: %q %q %v", stored, replaced, err)
	}
	if _, _, err := StoreLocalFiles(dst, map[string][]byte{"local:../x.json": []byte("x")}); err == nil {
		t.Error("stored a name a move does not carry")
	}
	removed, err := RemoveLocalFiles(dst, []string{"local:gitlab.json", "local:github.json", "local:github.json"})
	if err != nil || !slices.Equal(removed, []string{"core/local-secrets/gitlab.json", "core/local-secrets/github.json"}) {
		t.Errorf("removed %q: %v", removed, err)
	}
	if _, err := RemoveLocalFiles(dst, []string{"dashboard-users.json"}); err == nil {
		t.Error("removed a file of the credentials folder")
	}
}
