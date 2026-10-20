package cloud

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestValidKey(t *testing.T) {
	for _, k := range []string{"a", "a/b.txt", ".fridolin-vault/manifest.json", "x y/z"} {
		if err := ValidKey(k); err != nil {
			t.Errorf("ValidKey(%q) = %v", k, err)
		}
	}
	for _, k := range []string{"", "/a", "a/", "a//b", "../a", "a/./b", `a\b`, "a\x00"} {
		if ValidKey(k) == nil {
			t.Errorf("ValidKey(%q) accepted", k)
		}
	}
}

// storeContract runs the behaviour every ObjectStore promises.
func storeContract(t *testing.T, s ObjectStore) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Stat(ctx, "missing.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat(missing) = %v, want ErrNotFound", err)
	}
	if _, err := s.Get(ctx, "missing.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) = %v, want ErrNotFound", err)
	}
	if list, err := s.List(ctx, "nothing/here"); err != nil || len(list) != 0 {
		t.Fatalf("List(missing prefix) = %v, %v", list, err)
	}
	files := map[string]string{
		"a.txt":            "alpha",
		"dir/b.bin":        strings.Repeat("b", 300_000),
		"dir/sub/c.md":     "# c",
		"other/d with sp":  "",
		"dir/sub/deep/e.x": "e",
	}
	for k, v := range files {
		if err := s.Put(ctx, k, strings.NewReader(v)); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
	}
	// Replace one.
	files["a.txt"] = "alpha two"
	if err := s.Put(ctx, "a.txt", strings.NewReader(files["a.txt"])); err != nil {
		t.Fatal(err)
	}
	for k, v := range files {
		info, err := s.Stat(ctx, k)
		if err != nil || info.Size != int64(len(v)) || info.Key != k {
			t.Fatalf("Stat(%s) = %+v, %v", k, info, err)
		}
		rc, err := s.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get(%s): %v", k, err)
		}
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read %s: %v", k, err)
		}
		if err := rc.Close(); err != nil {
			t.Fatalf("close %s: %v", k, err)
		}
		if string(got) != v {
			t.Fatalf("Get(%s) = %d bytes, want %d", k, len(got), len(v))
		}
	}
	all, err := s.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, o := range all {
		keys = append(keys, o.Key)
	}
	want := []string{"a.txt", "dir/b.bin", "dir/sub/c.md", "dir/sub/deep/e.x", "other/d with sp"}
	if !slices.Equal(keys, want) {
		t.Fatalf("List = %v, want %v", keys, want)
	}
	sub, err := s.List(ctx, "dir/sub")
	if err != nil {
		t.Fatal(err)
	}
	keys = keys[:0]
	for _, o := range sub {
		keys = append(keys, o.Key)
	}
	if !slices.Equal(keys, []string{"dir/sub/c.md", "dir/sub/deep/e.x"}) {
		t.Fatalf("List(dir/sub) = %v", keys)
	}
	if err := s.Delete(ctx, "dir/b.bin"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "dir/b.bin"); err != nil {
		t.Fatalf("second Delete = %v, want nil", err)
	}
	if _, err := s.Stat(ctx, "dir/b.bin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat after Delete = %v", err)
	}
	if err := s.Put(ctx, "../escape", strings.NewReader("x")); err == nil {
		t.Fatal("Put accepted ../escape")
	}
}

func TestDirStore(t *testing.T) {
	d, err := NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storeContract(t, d)
}

func TestDirFailInjection(t *testing.T) {
	d, err := NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	d.Fail = func(op, key string) error {
		if op == "put" {
			return boom
		}
		return nil
	}
	if err := d.Put(context.Background(), "a", strings.NewReader("x")); !errors.Is(err, boom) {
		t.Fatalf("Put = %v", err)
	}
}

func TestObscureRoundTrip(t *testing.T) {
	for _, s := range []string{"", "p", "a much longer password with spaces and ünïcode"} {
		o, err := Obscure(s)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Reveal(o)
		if err != nil || got != s {
			t.Fatalf("Reveal(Obscure(%q)) = %q, %v", s, got, err)
		}
	}
	if _, err := Reveal("short"); err == nil {
		t.Fatal("Reveal of a short value succeeded")
	}
}

// TestObscureKnownVector pins the algorithm with a fixed IV, so a change
// to the key or the mode is caught without rclone installed.
func TestObscureKnownVector(t *testing.T) {
	iv := bytes.Repeat([]byte{0}, 16)
	got, err := obscureWith(bytes.NewReader(iv), "potato")
	if err != nil {
		t.Fatal(err)
	}
	// `rclone reveal` of this value prints "potato".
	const want = "AAAAAAAAAAAAAAAAAAAAADS03QMhkA"
	if got != want {
		t.Fatalf("Obscure with a zero IV = %q, want %q", got, want)
	}
	if rev, err := Reveal(want); err != nil || rev != "potato" {
		t.Fatalf("Reveal = %q, %v", rev, err)
	}
}

func rcloneBinary(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone is not installed")
	}
	return p
}

// TestObscureMatchesRclone checks both directions against the real rclone:
// rclone reveals what Obscure wrote, and Reveal reads what rclone obscured.
func TestObscureMatchesRclone(t *testing.T) {
	bin := rcloneBinary(t)
	env := []string{"HOME=" + t.TempDir()}
	for _, secret := range []string{"hunter2", "correct horse battery staple", "ü-ß"} {
		mine, err := Obscure(secret)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, "--config", "", "reveal", mine)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("rclone reveal: %v", err)
		}
		if got := strings.TrimRight(string(out), "\n"); got != secret {
			t.Fatalf("rclone reveal(Obscure(%q)) = %q", secret, got)
		}
		cmd = exec.Command(bin, "--config", "", "obscure", secret)
		cmd.Env = env
		out, err = cmd.Output()
		if err != nil {
			t.Fatalf("rclone obscure: %v", err)
		}
		got, err := Reveal(strings.TrimSpace(string(out)))
		if err != nil || got != secret {
			t.Fatalf("Reveal(rclone obscure %q) = %q, %v", secret, got, err)
		}
	}
}

func TestRemoteEnv(t *testing.T) {
	env, err := B2Env("vault", "keyid123", "secretkey")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"RCLONE_CONFIG_VAULT_ACCOUNT=keyid123",
		"RCLONE_CONFIG_VAULT_HARD_DELETE=false",
		"RCLONE_CONFIG_VAULT_KEY=secretkey",
		"RCLONE_CONFIG_VAULT_TYPE=b2",
	}
	if !slices.Equal(env, want) {
		t.Fatalf("B2Env = %v", env)
	}
	if _, err := RemoteEnv("Bad-Name", map[string]string{"type": "local"}); err == nil {
		t.Fatal("RemoteEnv accepted a bad name")
	}
	if _, err := RemoteEnv("ok", map[string]string{}); err == nil {
		t.Fatal("RemoteEnv accepted no type")
	}
	v := VaultRemote{Name: "vault", KeyID: "id", Key: "k", Bucket: "fridolin-vault", Path: "/vault/", Password: "pw", Password2: "salt"}
	env, remote, err := v.Env()
	if err != nil {
		t.Fatal(err)
	}
	if remote != "vaultcrypt:" {
		t.Fatalf("remote = %q", remote)
	}
	got := map[string]string{}
	for _, kv := range env {
		k, val, _ := strings.Cut(kv, "=")
		got[k] = val
	}
	if got["RCLONE_CONFIG_VAULTCRYPT_TYPE"] != "crypt" || got["RCLONE_CONFIG_VAULTCRYPT_REMOTE"] != "vault:fridolin-vault/vault" {
		t.Fatalf("crypt env = %v", got)
	}
	if pw, err := Reveal(got["RCLONE_CONFIG_VAULTCRYPT_PASSWORD"]); err != nil || pw != "pw" {
		t.Fatalf("password not obscured: %q %v", pw, err)
	}
	if pw, err := Reveal(got["RCLONE_CONFIG_VAULTCRYPT_PASSWORD2"]); err != nil || pw != "salt" {
		t.Fatalf("password2 not obscured: %q %v", pw, err)
	}
	if _, _, err := (VaultRemote{Name: "vault", KeyID: "id", Key: "k", Bucket: "b"}).Env(); err == nil {
		t.Fatal("a vault remote without a crypt password was accepted")
	}
}

func TestRcloneErrorRedactsSecrets(t *testing.T) {
	r := &Rclone{Env: []string{"RCLONE_CONFIG_X_TYPE=b2", "RCLONE_CONFIG_X_KEY=topsecretkey"}, Secrets: []string{"plainpw"}}
	got := r.redact("auth failed for topsecretkey using plainpw on b2")
	if strings.Contains(got, "topsecretkey") || strings.Contains(got, "plainpw") || !strings.Contains(got, "b2") {
		t.Fatalf("redact = %q", got)
	}
	long := strings.Repeat("x", 3*stderrLimit)
	if got := r.redact(long); len(got) > stderrLimit+len("…") {
		t.Fatalf("redact kept %d bytes", len(got))
	}
}

// localCryptStore is an Rclone store on a crypt remote over a local
// folder, configured only by environment variables.
func localCryptStore(t *testing.T) (*Rclone, string) {
	t.Helper()
	bin := rcloneBinary(t)
	under := t.TempDir()
	base, err := RemoteEnv("lcl", map[string]string{"type": "local"})
	if err != nil {
		t.Fatal(err)
	}
	crypt, err := CryptEnv("lclcrypt", "lcl:"+filepath.ToSlash(under), "integration password", "integration salt")
	if err != nil {
		t.Fatal(err)
	}
	env := append(append([]string{"HOME=" + t.TempDir()}, base...), crypt...)
	return &Rclone{Binary: bin, Remote: "lclcrypt:", Env: env, Secrets: []string{"integration password"}}, under
}

// TestRcloneCryptIntegration drives the real rclone against a crypt remote
// over a local folder: the remote exists only in environment variables,
// and what lands on disk has neither the names nor the contents in clear.
func TestRcloneCryptIntegration(t *testing.T) {
	r, under := localCryptStore(t)
	storeContract(t, r)

	ctx := context.Background()
	secret := "the plaintext that must not be on disk"
	if err := r.Put(ctx, "private/notes.md", strings.NewReader(secret)); err != nil {
		t.Fatal(err)
	}
	err := filepath.Walk(under, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		for _, clear := range []string{"private", "notes.md", "a.txt", "dir"} {
			if fi.Name() == clear {
				t.Errorf("clear name %q on the crypt backend: %s", clear, p)
			}
		}
		if fi.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte("alpha two")) {
				t.Errorf("clear contents on the crypt backend: %s", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// A wrong password is an rclone error that does not repeat it.
	bad := *r
	bad.Env = slices.Clone(r.Env)
	for i, kv := range bad.Env {
		if strings.HasPrefix(kv, "RCLONE_CONFIG_LCLCRYPT_PASSWORD=") {
			o, _ := Obscure("wrong password")
			bad.Env[i] = "RCLONE_CONFIG_LCLCRYPT_PASSWORD=" + o
		}
	}
	rc, err := bad.Get(ctx, "private/notes.md")
	if err == nil {
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		if string(data) == secret {
			t.Fatal("a wrong crypt password read the plaintext")
		}
	}

	// Cancelling the context stops a running rclone, even one waiting for
	// input that never comes.
	cctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	done := make(chan error, 1)
	go func() { done <- r.Put(cctx, "slow.bin", pr) }()
	go func() { _, _ = pw.Write([]byte("partial")) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Put = %v, want context.Canceled", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("cancelled Put did not return")
	}

	// Closing a Get early is not an error.
	if err := r.Put(ctx, "big.bin", bytes.NewReader(bytes.Repeat([]byte("z"), 4<<20))); err != nil {
		t.Fatal(err)
	}
	rc, err = r.Get(ctx, "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 10)
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("early Close = %v", err)
	}
}
