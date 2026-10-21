package hostsecrets

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
)

// fakeCred is the Credential Manager in memory.
type fakeCred struct {
	blobs map[string][]byte
	users map[string]string
}

func newFakeCred() *fakeCred {
	return &fakeCred{blobs: map[string][]byte{}, users: map[string]string{}}
}

func (f *fakeCred) Read(target string) ([]byte, error) {
	b, ok := f.blobs[target]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), b...), nil
}

func (f *fakeCred) Write(target, user string, blob []byte) error {
	f.blobs[target], f.users[target] = append([]byte(nil), blob...), user
	return nil
}

func (f *fakeCred) Delete(target string) error {
	if _, ok := f.blobs[target]; !ok {
		return ErrNotFound
	}
	delete(f.blobs, target)
	return nil
}

func utf16le(s string) []byte {
	var out []byte
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

func TestWinCred(t *testing.T) {
	fake := newFakeCred()
	w := WinCred{API: fake}
	if ok, err := w.Has(AgeIdentity); ok || err != nil {
		t.Fatalf("Has on an empty store: %v %v", ok, err)
	}
	if _, err := w.Get(AgeIdentity); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get: %v", err)
	}
	const secret = "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ"
	if err := w.Set(AgeIdentity, secret); err != nil {
		t.Fatal(err)
	}
	if got, err := w.Get(AgeIdentity); err != nil || got != secret {
		t.Fatalf("Get %q, %v", got, err)
	}
	if string(fake.blobs[AgeIdentity]) != secret || fake.users[AgeIdentity] != Account {
		t.Errorf("stored %q for user %q", fake.blobs[AgeIdentity], fake.users[AgeIdentity])
	}
	// What cmdkey and the Control Panel store: UTF-16LE.
	fake.blobs[ClaudeOAuthToken] = utf16le("token-abc_DEF+/=")
	if got, err := w.Get(ClaudeOAuthToken); err != nil || got != "token-abc_DEF+/=" {
		t.Errorf("a UTF-16 blob: %q, %v", got, err)
	}
	fake.blobs[ResticPassword] = []byte("pw\r\n")
	if got, _ := w.Get(ResticPassword); got != "pw" {
		t.Errorf("a trailing line end was kept: %q", got)
	}
	if err := w.Set(TailscaleAuthKey, ""); err == nil {
		t.Error("stored an empty value")
	}
	if err := w.Set(TailscaleAuthKey, strings.Repeat("x", maxCredBlob+1)); err == nil {
		t.Error("stored a value longer than the Credential Manager keeps")
	}
	if err := w.Set("../bad", "x"); err == nil {
		t.Error("accepted a bad name")
	}
	if err := w.Delete(AgeIdentity); err != nil {
		t.Fatal(err)
	}
	if err := w.Delete(AgeIdentity); err != nil {
		t.Errorf("deleting a missing secret: %v", err)
	}
	if ok, _ := w.Has(AgeIdentity); ok {
		t.Error("still there after Delete")
	}
	failing := WinCred{API: errCred{}}
	if _, err := failing.Has(AgeIdentity); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Errorf("Has hid an error: %v", err)
	}
}

type errCred struct{}

func (errCred) Read(string) ([]byte, error)        { return nil, errors.New("access denied") }
func (errCred) Write(string, string, []byte) error { return errors.New("access denied") }
func (errCred) Delete(string) error                { return errors.New("access denied") }

func TestDecodeCredBlob(t *testing.T) {
	for in, want := range map[string]string{
		"":                          "",
		"a":                         "a",
		"ab":                        "ab",
		string(utf16le("ab")):       "ab",
		string(utf16le("tskey-1")):  "tskey-1",
		"a\x00\x00\x00":             "a\x00\x00\x00",
		"AGE-SECRET-KEY-1ABCDEFGHI": "AGE-SECRET-KEY-1ABCDEFGHI",
	} {
		if got := decodeCredBlob([]byte(in)); got != want {
			t.Errorf("decodeCredBlob(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestForHost(t *testing.T) {
	getenv := func(name string) string {
		if name == "XDG_CONFIG_HOME" {
			return "/x"
		}
		return ""
	}
	if _, ok := ForHost("darwin", getenv, "/home/you").(Keychain); !ok {
		t.Error("macOS: not the keychain")
	}
	if _, ok := ForHost("windows", getenv, `C:\Users\you`).(WinCred); !ok {
		t.Error("Windows: not the Credential Manager")
	}
	if f, ok := ForHost("linux", getenv, "/home/you").(Files); !ok || f.Dir != "/x/fridolin/secrets" && f.Dir != `\x\fridolin\secrets` {
		t.Errorf("Linux: %#v", ForHost("linux", getenv, "/home/you"))
	}
	if !strings.Contains(Place("windows", getenv, ""), "Credential Manager") {
		t.Error(Place("windows", getenv, ""))
	}
}

// TestSystemCredManager writes, reads and removes a credential of its own
// in this user's Credential Manager; elsewhere it checks that the system
// store says it is Windows only.
func TestSystemCredManager(t *testing.T) {
	w := WinCred{}
	if runtime.GOOS != "windows" {
		if _, err := w.Get(AgeIdentity); err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "only on Windows") {
			t.Errorf("Get off Windows: %v", err)
		}
		return
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "fridolin.test." + hex.EncodeToString(b)
	t.Cleanup(func() { _ = w.Delete(name) })
	if ok, err := w.Has(name); ok || err != nil {
		t.Fatalf("Has before Set: %v %v", ok, err)
	}
	const value = "AGE-SECRET-KEY-1TESTVALUE+/=_:@-"
	if err := w.Set(name, value); err != nil {
		t.Fatal(err)
	}
	if got, err := w.Get(name); err != nil || got != value {
		t.Fatalf("Get %q, %v", got, err)
	}
	if err := w.Set(name, value+"2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.Get(name); got != value+"2" {
		t.Errorf("Set did not replace: %q", got)
	}
	if err := w.Delete(name); err != nil {
		t.Fatal(err)
	}
	if ok, err := w.Has(name); ok || err != nil {
		t.Errorf("Has after Delete: %v %v", ok, err)
	}
}
