package hostsecrets

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fakeSecurity records the calls and answers like /usr/bin/security.
type fakeSecurity struct {
	items map[string]string
	calls [][]string
	stdin []string
}

func (f *fakeSecurity) run(_ context.Context, _ string, args []string, stdin io.Reader) ([]byte, error) {
	f.calls = append(f.calls, args)
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.stdin = append(f.stdin, string(b))
		fields := strings.Fields(string(b))
		svc := fields[slices.Index(fields, "-s")+1]
		val := fields[slices.Index(fields, "-w")+1]
		f.items[svc] = strings.Trim(val, `"`)
		return nil, nil
	}
	svc := args[slices.Index(args, "-s")+1]
	v, ok := f.items[svc]
	if !ok {
		return nil, &ExitError{Code: 44, Stderr: "The specified item could not be found in the keychain."}
	}
	return []byte(v + "\n"), nil
}

func TestKeychain(t *testing.T) {
	fake := &fakeSecurity{items: map[string]string{}}
	k := Keychain{Run: fake.run}
	if ok, err := k.Has(AgeIdentity); ok || err != nil {
		t.Fatalf("Has on an empty keychain: %v %v", ok, err)
	}
	if _, err := k.Get(AgeIdentity); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get: %v", err)
	}
	const secret = "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ"
	if err := k.Set(AgeIdentity, secret); err != nil {
		t.Fatal(err)
	}
	if got, err := k.Get(AgeIdentity); err != nil || got != secret {
		t.Fatalf("Get %q, %v", got, err)
	}
	for _, args := range fake.calls {
		if slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, secret) }) {
			t.Errorf("the secret is on a command line: %q", args)
		}
	}
	if len(fake.stdin) != 1 || !strings.Contains(fake.stdin[0], "-a fridolin -s fridolin.age.identity") {
		t.Errorf("stdin %q", fake.stdin)
	}
	if err := k.Set(AgeIdentity, "has \" quote"); err == nil {
		t.Error("passed a value with a quote to security -i")
	}
	if _, err := k.Get("bad name"); err == nil {
		t.Error("accepted a bad name")
	}
	failing := Keychain{Run: func(context.Context, string, []string, io.Reader) ([]byte, error) {
		return nil, &ExitError{Code: 51, Stderr: "User interaction is not allowed."}
	}}
	if _, err := failing.Has(AgeIdentity); err == nil || !strings.Contains(err.Error(), "interaction") {
		t.Errorf("Has hid an error: %v", err)
	}
}

func TestFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	f := Files{Dir: dir}
	if ok, err := f.Has(TailscaleAuthKey); ok || err != nil {
		t.Fatalf("Has: %v %v", ok, err)
	}
	if err := f.Set(TailscaleAuthKey, "tskey-auth-xyz"); err != nil {
		t.Fatal(err)
	}
	if got, err := f.Get(TailscaleAuthKey); err != nil || got != "tskey-auth-xyz" {
		t.Fatalf("Get %q %v", got, err)
	}
	if err := f.Set("../escape", "x"); err == nil {
		t.Error("wrote outside the folder")
	}
	if runtime.GOOS == "windows" {
		return // no Unix modes; Windows keeps its secrets in the Credential Manager
	}
	st, _ := os.Stat(filepath.Join(dir, TailscaleAuthKey))
	dst, _ := os.Stat(dir)
	if st.Mode().Perm() != 0o600 || dst.Mode().Perm() != 0o700 {
		t.Errorf("modes %v %v", st.Mode(), dst.Mode())
	}
	if err := os.Chmod(filepath.Join(dir, TailscaleAuthKey), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Get(TailscaleAuthKey); err == nil {
		t.Error("read a secret others may read")
	}
}

func TestExecRunReportsTheExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /bin/sh; the keychain helper is macOS's")
	}
	_, err := execRun(context.Background(), "/bin/sh", []string{"-c", "echo nope >&2; exit 44"}, nil)
	ee, ok := errors.AsType[*ExitError](err)
	if !ok || ee.Code != 44 || ee.Stderr != "nope" {
		t.Errorf("error %v", err)
	}
}
