package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/snapshot"
)

// scriptedRclone answers each rclone subcommand with what answers holds
// for it and records the calls.
type scriptedRclone struct {
	answers map[string]struct {
		out string
		err error
	}
	calls []string
}

func (r *scriptedRclone) run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, strings.Join(args, " "))
	a := r.answers[args[0]]
	return a.out, a.err
}

func (r *scriptedRclone) answer(op, out string, err error) {
	if r.answers == nil {
		r.answers = map[string]struct {
			out string
			err error
		}{}
	}
	r.answers[op] = struct {
		out string
		err error
	}{out, err}
}

func TestRcloneRetire(t *testing.T) {
	notFound := errors.New("rclone purge: exit status 3: ERROR : error listing: directory not found")
	refused := errors.New("rclone purge: exit status 1: googleapi: Error 403: The user may not have granted the app 202264815644 write access to all of the children of file X., appNotAuthorizedToChild")
	ctx := context.Background()

	t.Run("purged", func(t *testing.T) {
		r := &scriptedRclone{}
		left, err := rcloneRetire(ctx, r.run, "brain-gdrive:fridolin-restic", "fridolin-restic")
		if err != nil || left != "" || len(r.calls) != 1 {
			t.Errorf("%q %v %v", left, err, r.calls)
		}
	})
	t.Run("B2 folder already gone", func(t *testing.T) {
		r := &scriptedRclone{}
		r.answer("purge", "", notFound)
		left, err := rcloneRetire(ctx, r.run, "fridolinsnap:bucket/restic", "")
		if err != nil || left != "" {
			t.Errorf("%q %v", left, err)
		}
	})
	t.Run("Drive folder not seen by this client", func(t *testing.T) {
		r := &scriptedRclone{}
		r.answer("purge", "", notFound)
		left, err := rcloneRetire(ctx, r.run, "brain-gdrive:fridolin-restic", "fridolin-restic")
		if err != nil || !strings.Contains(left, `"fridolin-restic"`) || !strings.Contains(left, "by hand") {
			t.Errorf("%q %v", left, err)
		}
	})
	t.Run("Drive refuses the folder, nothing reachable left", func(t *testing.T) {
		r := &scriptedRclone{}
		r.answer("purge", "", refused)
		r.answer("rmdirs", "", refused)
		left, err := rcloneRetire(ctx, r.run, "brain-gdrive:fridolin-restic", "fridolin-restic")
		if err != nil || !strings.Contains(left, "did not let fridolin delete") || !strings.Contains(left, `"fridolin-restic"`) {
			t.Errorf("%q %v", left, err)
		}
		if want := "purge,delete,rmdirs,lsf"; opsOf(r.calls) != want {
			t.Errorf("calls %v, want %s", r.calls, want)
		}
	})
	t.Run("Drive refuses, the folder goes with rmdirs", func(t *testing.T) {
		r := &scriptedRclone{}
		r.answer("purge", "", refused)
		r.answer("lsf", "", notFound)
		left, err := rcloneRetire(ctx, r.run, "brain-gdrive:fridolin-restic", "fridolin-restic")
		if err != nil || left != "" {
			t.Errorf("%q %v", left, err)
		}
	})
	t.Run("Drive refuses and files are left", func(t *testing.T) {
		r := &scriptedRclone{}
		r.answer("purge", "", refused)
		r.answer("lsf", "data/aa\ndata/bb\n", nil)
		if _, err := rcloneRetire(ctx, r.run, "brain-gdrive:fridolin-restic", "fridolin-restic"); err == nil || !strings.Contains(err.Error(), "2 files are left") {
			t.Errorf("err %v", err)
		}
	})
	t.Run("a refusal on B2 is an error", func(t *testing.T) {
		r := &scriptedRclone{}
		r.answer("purge", "", refused)
		if _, err := rcloneRetire(ctx, r.run, "fridolinsnap:bucket/restic", ""); err == nil || len(r.calls) != 1 {
			t.Errorf("err %v, calls %v", err, r.calls)
		}
	})
	t.Run("another error", func(t *testing.T) {
		r := &scriptedRclone{}
		r.answer("purge", "", errors.New("rclone purge: exit status 1: couldn't connect"))
		if _, err := rcloneRetire(ctx, r.run, "brain-gdrive:fridolin-restic", "fridolin-restic"); err == nil {
			t.Error("no error")
		}
	})
}

func opsOf(calls []string) string {
	var ops []string
	for _, c := range calls {
		op, _, _ := strings.Cut(c, " ")
		ops = append(ops, op)
	}
	return strings.Join(ops, ",")
}

// TestExecRcloneMasksSecrets: what rclone says comes back with the
// secrets masked.
func TestExecRcloneMasksSecrets(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "rclone")
	writeFile(t, bin, "#!/bin/sh\necho \"failed with s3cret-value\" >&2\nexit 1\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := execRclone(bin, nil, []string{"s3cret-value"})(context.Background(), "purge", "x:y")
	if err == nil || strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "rclone purge x:y") {
		t.Errorf("err %v", err)
	}
}

// TestRetiredWithLeftover: a store retired with something left for the
// owner is no longer "copying", and says what is left.
func TestRetiredWithLeftover(t *testing.T) {
	st := encryptionState{Retired: map[string]time.Time{"drive": time.Now()}, Left: map[string]string{"drive": "delete it"}}
	b := vaultBackends{restic: &snapshot.Restic{}}
	for _, l := range describeLayers(b, vaultCreds{SnapCryptPassword: "set"}, st, true) {
		switch l.Store {
		case "drive":
			if l.Old != "retired" || l.Left != "delete it" {
				t.Errorf("drive: %+v", l)
			}
		case "restic":
			if l.Left != "" {
				t.Errorf("restic: %+v", l)
			}
		}
	}
	dir := t.TempDir()
	if err := writeEncryptionState(dir, st); err != nil {
		t.Fatal(err)
	}
	back, err := readEncryptionState(dir)
	if err != nil || back.Left["drive"] != "delete it" {
		t.Errorf("%+v %v", back, err)
	}
}
