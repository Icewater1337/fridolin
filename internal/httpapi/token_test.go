package httpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDashboardToken(t *testing.T) {
	t.Run("made on first run, then kept", func(t *testing.T) {
		dir := t.TempDir()
		a, err := LoadDashboardToken(dir)
		if err != nil {
			t.Fatal(err)
		}
		b, err := LoadDashboardToken(dir)
		if err != nil || b.Value() != a.Value() || len(a.Value()) < minTokenLen {
			t.Fatalf("second load: %v, %q vs %q", err, b.Value(), a.Value())
		}
	})
	cases := []struct {
		name  string
		setup func(path string) error
		want  string
	}{
		{"too short", func(p string) error { return os.WriteFile(p, []byte("abc\n"), 0o600) }, "is not a token"},
		{"two words", func(p string) error {
			return os.WriteFile(p, []byte(strings.Repeat("a", 20)+" "+strings.Repeat("b", 20)), 0o600)
		}, "is not a token"},
		{"unreadable", func(p string) error { return os.Mkdir(p, 0o700) }, "reading token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "dashboard.token")
			if err := c.setup(path); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			if _, err := LoadDashboardToken(dir); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			if after, _ := os.ReadFile(path); string(after) != string(before) {
				t.Fatal("the token file was replaced")
			}
		})
	}
}

func TestHTTPTokenReadAndRotate(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadToken(dir); !os.IsNotExist(err) {
		t.Fatalf("missing read=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dashboard.token")); !os.IsNotExist(err) {
		t.Fatalf("read created a token: %v", err)
	}
	token, err := LoadDashboardToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := token.Value()
	if err := token.Rotate(); err != nil {
		t.Fatal(err)
	}
	if token.Matches(old) || !token.Matches(token.Value()) {
		t.Fatal("rotation did not replace the active token")
	}
	got, err := ReadToken(dir)
	if err != nil || got != token.Value() {
		t.Fatalf("persisted token disagrees: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "dashboard.token"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token permissions=%v error=%v", info, err)
	}
}
