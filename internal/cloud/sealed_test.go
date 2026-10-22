package cloud

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/seal"
)

func newSealedDir(t *testing.T, key string) (*Sealed, *Dir) {
	t.Helper()
	d, err := NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSealed(d, key)
	if err != nil {
		t.Fatal(err)
	}
	return s, d
}

func TestSealedContract(t *testing.T) {
	key, _ := seal.NewKey()
	s, _ := newSealedDir(t, key)
	storeContract(t, s)
}

func TestSealedStoresCiphertextAndPlainSizes(t *testing.T) {
	key, _ := seal.NewKey()
	s, d := newSealedDir(t, key)
	ctx := context.Background()
	body := strings.Repeat("a secret line\n", 9000)
	if err := s.Put(ctx, "docs/a.md", strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(d.Root, "docs", "a.md"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("a secret line")) {
		t.Fatal("the inner store holds the plaintext")
	}
	info, err := s.Stat(ctx, "docs/a.md")
	if err != nil || info.Size != int64(len(body)) {
		t.Fatalf("Stat: %+v %v, want the plaintext's size %d", info, err, len(body))
	}
	list, err := s.List(ctx, "docs")
	if err != nil || len(list) != 1 || list[0].Size != int64(len(body)) {
		t.Fatalf("List: %+v %v", list, err)
	}
	// A wrong key fails closed, and so does an object that is not sealed.
	other, _ := seal.NewKey()
	wrong, _ := NewSealed(d, other)
	if _, err := wrong.Get(ctx, "docs/a.md"); !errors.Is(err, seal.ErrWrongKey) {
		t.Fatalf("wrong key: %v", err)
	}
	if err := d.Put(ctx, "plain.txt", strings.NewReader("not sealed at all, but long enough for a header")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "plain.txt"); !errors.Is(err, seal.ErrNotSealed) {
		t.Fatalf("unsealed object: %v", err)
	}
	// A damaged object fails on Read, never passing on a part as whole.
	raw[len(raw)-1] ^= 1
	if err := os.WriteFile(filepath.Join(d.Root, "docs", "a.md"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	rc, err := s.Get(ctx, "docs/a.md")
	if err == nil {
		_, err = io.ReadAll(rc)
		rc.Close()
	}
	if !errors.Is(err, seal.ErrDamaged) {
		t.Fatalf("damaged: %v", err)
	}
	if err := DeleteKeys(ctx, s, []string{"docs/a.md", "plain.txt", "never/there"}); err != nil {
		t.Fatal(err)
	}
	if left, _ := d.List(ctx, ""); len(left) != 0 {
		t.Fatalf("left after DeleteKeys: %v", left)
	}
}

// TestRcloneDaemonDeleteKeys deletes a batch through rclone rcd, leaving
// what is not named, keys with glob characters included.
func TestRcloneDaemonDeleteKeys(t *testing.T) {
	r, _ := localCryptDaemon(t)
	ctx := context.Background()
	keys := []string{"ltx/0/1-2.ltx", "ltx/0/3-4.ltx", "ltx/1/a[1].ltx", "keep/me", "ltx/0/5-6.ltx"}
	for _, k := range keys {
		if err := r.Put(ctx, k, strings.NewReader(k)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.DeleteKeys(ctx, []string{"ltx/0/1-2.ltx", "ltx/1/a[1].ltx", "not/there"}); err != nil {
		t.Fatal(err)
	}
	list, err := r.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range list {
		got = append(got, o.Key)
	}
	if strings.Join(got, ",") != "keep/me,ltx/0/3-4.ltx,ltx/0/5-6.ltx" {
		t.Fatalf("left: %v", got)
	}
}
