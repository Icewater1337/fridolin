package rcrypt

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func cipherFor(t testing.TB, pw, salt string) *Cipher {
	t.Helper()
	c, err := New(pw, salt)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRoundTripAndRanges(t *testing.T) {
	c := cipherFor(t, "correct horse", "")
	for _, n := range []int{0, 1, BlockSize - 1, BlockSize, BlockSize + 1, 3*BlockSize + 100} {
		data := make([]byte, n)
		_, _ = rand.Read(data)
		enc, err := c.Encrypt(data)
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(enc)) != EncryptedSize(int64(n)) {
			t.Errorf("%d bytes: encrypted %d, EncryptedSize %d", n, len(enc), EncryptedSize(int64(n)))
		}
		if p, err := DecryptedSize(int64(len(enc))); err != nil || p != int64(n) {
			t.Errorf("%d bytes: DecryptedSize %d %v", n, p, err)
		}
		got, err := c.Decrypt(enc)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%d bytes: round trip: %v", n, err)
		}
		readAt := func(off, l int64) ([]byte, error) { return enc[off:min(int64(len(enc)), off+l)], nil }
		for _, r := range [][2]int64{{0, 10}, {int64(n) / 2, int64(n) / 3}, {BlockSize - 3, 7}, {int64(n), 5}, {int64(max(0, n-1)), -1}} {
			if r[0] > int64(n) {
				continue
			}
			got, err := c.DecryptRange(readAt, int64(len(enc)), r[0], r[1])
			if err != nil {
				t.Fatalf("%d bytes, range %v: %v", n, r, err)
			}
			end := int64(n)
			if r[1] >= 0 {
				end = min(end, r[0]+r[1])
			}
			if !bytes.Equal(got, data[r[0]:end]) {
				t.Fatalf("%d bytes, range %v: wrong bytes", n, r)
			}
		}
	}
}

func TestWrongPasswordFailsClosed(t *testing.T) {
	enc, err := cipherFor(t, "one", "salt").Encrypt([]byte("restic's ciphertext"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cipherFor(t, "two", "salt").Decrypt(enc); !errors.Is(err, ErrBadBlock) {
		t.Fatalf("another password: %v", err)
	}
	if _, err := cipherFor(t, "one", "pepper").Decrypt(enc); !errors.Is(err, ErrBadBlock) {
		t.Fatalf("another salt: %v", err)
	}
	enc[len(enc)-1] ^= 1
	if _, err := cipherFor(t, "one", "salt").Decrypt(enc); !errors.Is(err, ErrBadBlock) {
		t.Fatalf("a flipped byte: %v", err)
	}
	if _, err := cipherFor(t, "one", "salt").Decrypt(append([]byte("NOTCLONE"), enc[8:]...)); !errors.Is(err, ErrBadHeader) {
		t.Fatalf("another header: %v", err)
	}
	if _, err := New("", ""); err == nil {
		t.Fatal("an empty password was taken")
	}
}

func TestDecryptedSizeRefusesImpossibleSizes(t *testing.T) {
	for _, n := range []int64{0, int64(HeaderSize) - 1, int64(HeaderSize) + tagSize, int64(HeaderSize) + 1} {
		if _, err := DecryptedSize(n); err == nil {
			t.Errorf("DecryptedSize(%d) took it", n)
		}
	}
}

func TestNonceAdd(t *testing.T) {
	var a, b nonce
	for i := range nonceSize {
		a[i], b[i] = 0xff, 0xff
	}
	a[10] = 3
	b[10] = 3
	for range 300 {
		a.increment()
	}
	b.add(300)
	if a != b {
		t.Fatalf("add(300) %x, 300 increments %x", b, a)
	}
}

// TestRcloneReadsAndWrites checks the format against the real rclone, in
// both directions: rclone's crypt remote over a folder (no name
// encryption, no suffix) reads what Encrypt wrote, and DecryptRange reads
// what rclone wrote.
func TestRcloneReadsAndWrites(t *testing.T) {
	rclone, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone is not installed")
	}
	dir := t.TempDir()
	pw, salt := "a password from the recovery sheet", "its salt"
	obscure := func(s string) string {
		out, err := exec.Command(rclone, "obscure", s).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	env := append(os.Environ(), "RCLONE_CONFIG=/dev/null",
		"RCLONE_CONFIG_NODECRYPT_TYPE=crypt", "RCLONE_CONFIG_NODECRYPT_REMOTE="+dir,
		"RCLONE_CONFIG_NODECRYPT_FILENAME_ENCRYPTION=off", "RCLONE_CONFIG_NODECRYPT_SUFFIX=none",
		"RCLONE_CONFIG_NODECRYPT_PASSWORD="+obscure(pw), "RCLONE_CONFIG_NODECRYPT_PASSWORD2="+obscure(salt))
	c := cipherFor(t, pw, salt)
	data := make([]byte, 2*BlockSize+333)
	_, _ = rand.Read(data)
	enc, err := c.Encrypt(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "data", "ab"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", "ab", "abcdef"), enc, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(rclone, "cat", "nodecrypt:data/ab/abcdef")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rclone cat: %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Fatal("rclone read other bytes than were encrypted")
	}
	cmd = exec.Command(rclone, "rcat", "nodecrypt:config")
	cmd.Env, cmd.Stdin = env, bytes.NewReader(data)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rclone rcat: %v: %s", err, out)
	}
	written, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Decrypt(written)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("reading what rclone wrote: %v", err)
	}
}

func FuzzDecrypt(f *testing.F) {
	c := cipherFor(f, "pw", "")
	enc, _ := c.Encrypt([]byte("hello"))
	f.Add(enc, int64(0), int64(-1))
	f.Add([]byte(magic+"012345678901234567890123"), int64(1), int64(3))
	f.Fuzz(func(t *testing.T, data []byte, off, n int64) {
		readAt := func(o, l int64) ([]byte, error) {
			if o < 0 || o > int64(len(data)) {
				return nil, nil
			}
			return data[o:min(int64(len(data)), o+max(0, l))], nil
		}
		got, err := c.DecryptRange(readAt, int64(len(data)), off, n)
		if err == nil && n >= 0 && int64(len(got)) > n {
			t.Fatalf("asked for %d bytes, got %d", n, len(got))
		}
	})
}
