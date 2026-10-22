package app

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/Icewater1337/fridolin/internal/snapshot"
)

// TestTargetSecrets: a target's password comes from the environment, the
// host's store, or, in the container, an age file in secrets/; it is made
// only while the repository does not exist.
func TestTargetSecrets(t *testing.T) {
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FRIDOLIN_AGE_IDENTITY_PQ": id.String()}
	dir := t.TempDir()
	s := targetSecrets{getenv: func(k string) string { return env[k] }, secretsDir: dir,
		random: func() (string, error) { return "new-password-0123456789abcdef", nil }, log: slog.New(slog.DiscardHandler)}
	none := func(context.Context, string) (bool, error) { return false, nil }
	pw, err := s.ensure(context.Background(), "nas", none)
	if err != nil || pw != "new-password-0123456789abcdef" {
		t.Fatalf("made: %q %v", pw, err)
	}
	data, err := os.ReadFile(s.ageFile("nas"))
	if err != nil || strings.Contains(string(data), pw) {
		t.Fatalf("the age file holds the password encrypted: %v", err)
	}
	if got, err := s.get("nas"); err != nil || got != pw {
		t.Fatalf("read back: %q %v", got, err)
	}
	if !strings.Contains(string(data), "mlkem768x25519") {
		t.Error("the age file is not encrypted to the post-quantum identity")
	}
	// A file made for the X25519 identity of before still opens.
	old, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	w, _ := age.Encrypt(&buf, old.Recipient())
	_, _ = w.Write([]byte("old-password\n"))
	_ = w.Close()
	if err := os.WriteFile(s.ageFile("old"), []byte(buf.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	env["FRIDOLIN_AGE_IDENTITY"] = old.String()
	if got, err := s.get("old"); err != nil || got != "old-password" {
		t.Fatalf("an X25519 file: %q %v", got, err)
	}
	env["FRIDOLIN_RESTIC_NODE_MY_NAS"] = "from-env"
	if got, _ := s.get("my-nas"); got != "from-env" {
		t.Fatalf("the environment first: %q", got)
	}
	store := mapSecrets{}
	s.store = store
	if _, err := s.ensure(context.Background(), "other", func(context.Context, string) (bool, error) { return false, snapshot.ErrWrongPassword }); err == nil {
		t.Fatal("a repository that exists without its password")
	}
	if pw, err := s.ensure(context.Background(), "other", none); err != nil || store[targetSecretName("other")] != pw {
		t.Fatalf("into the host's store: %v %v", store, err)
	}
}
