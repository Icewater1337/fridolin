package snapshot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeRestic is a /bin/sh script standing in for restic. It records its
// arguments and environment, prints the files stdout and stderr of its
// folder and exits with the code in the file code.
type fakeRestic struct {
	t   *testing.T
	dir string
	bin string
}

func newFake(t *testing.T) *fakeRestic {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake binary is a shell script")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
D='` + dir + `'
for a in "$@"; do printf '%s\n' "$a"; done > "$D/args"
/usr/bin/env > "$D/env"
if [ -f "$D/stdout" ]; then /bin/cat "$D/stdout"; fi
if [ -f "$D/stderr" ]; then /bin/cat "$D/stderr" >&2; fi
code=0
if [ -f "$D/code" ]; then read -r code < "$D/code"; fi
exit "$code"
`
	bin := filepath.Join(dir, "restic")
	// cp writes the executable, not this process: a write handle here could
	// be inherited by a parallel test's fork until its exec, and exec of a
	// file open for writing fails with ETXTBSY ("text file busy") on Linux.
	src := filepath.Join(dir, "restic.src")
	if err := os.WriteFile(src, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cp", src, bin).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
	if err := os.Chmod(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	return &fakeRestic{t: t, dir: dir, bin: bin}
}

func (f *fakeRestic) set(name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeRestic) args() []string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "args"))
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// env returns the child's environment, without the variables the shell
// sets itself.
func (f *fakeRestic) env() []string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "env"))
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for kv := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "PWD", "OLDPWD", "SHLVL", "_", "__CF_USER_TEXT_ENCODING":
			continue
		}
		out = append(out, kv)
	}
	slices.Sort(out)
	return out
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

func TestResticArgs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := []string{"RESTIC_PASSWORD=dst-pw", "PATH=/bin", "B2_ACCOUNT_ID=acct", "B2_ACCOUNT_KEY=b2key"}
	newR := func(f *fakeRestic) *Restic {
		return &Restic{Binary: f.bin, Repo: "b2:bucket:dst", Env: env, Options: []string{"b2.connections=4"}}
	}
	from := &Restic{Repo: "b2:bucket:src", Env: []string{"RESTIC_PASSWORD=src-pw", "PATH=/bin"}, Options: []string{"rclone.program=/r"}}
	global := []string{"-r", "b2:bucket:dst", "--no-cache", "-o", "b2.connections=4"}

	for _, tc := range []struct {
		name    string
		stdout  string
		run     func(*Restic) error
		args    []string
		env     []string // nil: env
		options []string // nil: the global options
	}{
		{name: "exists", run: func(r *Restic) error {
			ok, err := r.Exists(ctx)
			if !ok {
				t.Error("Exists = false on exit 0")
			}
			return err
		}, args: []string{"cat", "config"}},
		{name: "init", run: func(r *Restic) error { return r.Init(ctx, nil) }, args: []string{"init"}},
		{
			name: "init from", run: func(r *Restic) error { return r.Init(ctx, from) },
			args:    []string{"init", "--from-repo", "b2:bucket:src", "--copy-chunker-params"},
			env:     append(slices.Clone(env), "RESTIC_FROM_PASSWORD=src-pw"),
			options: []string{"-r", "b2:bucket:dst", "--no-cache", "-o", "b2.connections=4", "-o", "rclone.program=/r"},
		},
		{
			name:   "backup",
			stdout: `{"message_type":"status","percent_done":0.5}` + "\n" + `{"message_type":"summary","files_new":2,"files_changed":1,"files_unmodified":3,"total_files_processed":6,"total_bytes_processed":600,"data_added":42,"snapshot_id":"abc"}` + "\n",
			run: func(r *Restic) error {
				s, err := r.Backup(ctx, BackupOptions{
					Paths: []string{"/p1", "-p2"}, Tags: []string{"t1", "t2"}, Excludes: []string{"*.tmp"},
					Host: "h", ExcludeCaches: true,
				})
				want := BackupSummary{SnapshotID: "abc", FilesNew: 2, FilesChanged: 1, FilesUnmodified: 3, TotalFilesProcessed: 6, TotalBytesProcessed: 600, DataAdded: 42}
				if s != want {
					t.Errorf("summary = %+v, want %+v", s, want)
				}
				return err
			},
			args: []string{"backup", "--json", "--host", "h", "--tag", "t1", "--tag", "t2", "--exclude", "*.tmp", "--exclude-caches", "--", "/p1", "-p2"},
		},
		{
			name:   "snapshots",
			stdout: `[{"id":"b","time":"2026-10-02T00:00:00Z","tags":["a","b"]},{"id":"a","time":"2026-10-01T00:00:00Z","summary":{"total_files_processed":3}}]`,
			run: func(r *Restic) error {
				snaps, err := r.Snapshots(ctx, []string{"a", "b"})
				if len(snaps) != 2 || snaps[0].ID != "a" || snaps[1].ID != "b" || snaps[0].Summary.TotalFilesProcessed != 3 {
					t.Errorf("snapshots = %+v, want a then b", snaps)
				}
				return err
			},
			args: []string{"snapshots", "--json", "--tag", "a,b"},
		},
		{
			name: "forget",
			run: func(r *Restic) error {
				return r.Forget(ctx, Retention{Hourly: 24, Daily: 7, Monthly: 12}, []string{"core"}, true)
			},
			args: []string{"forget", "--group-by", "host,tags", "--keep-hourly", "24", "--keep-daily", "7", "--keep-monthly", "12", "--tag", "core", "--prune"},
		},
		{
			name: "forget weekly", run: func(r *Restic) error { return r.Forget(ctx, Retention{Weekly: 4}, nil, false) },
			args: []string{"forget", "--group-by", "host,tags", "--keep-weekly", "4"},
		},
		{name: "check", run: func(r *Restic) error { return r.Check(ctx, "") }, args: []string{"check"}},
		{name: "check subset", run: func(r *Restic) error { return r.Check(ctx, "5%") }, args: []string{"check", "--read-data-subset=5%"}},
		{name: "restore", run: func(r *Restic) error { return r.Restore(ctx, "abc123", t.TempDir()) }, args: []string{"restore", "abc123", "--target"}},
		{
			name: "copy", run: func(r *Restic) error { return r.Copy(ctx, from) },
			args:    []string{"copy", "--from-repo", "b2:bucket:src"},
			env:     append(slices.Clone(env), "RESTIC_FROM_PASSWORD=src-pw"),
			options: []string{"-r", "b2:bucket:dst", "--no-cache", "-o", "b2.connections=4", "-o", "rclone.program=/r"},
		},
		{name: "unlock", run: func(r *Restic) error { return r.Unlock(ctx) }, args: []string{"unlock"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFake(t)
			if tc.stdout != "" {
				f.set("stdout", tc.stdout)
			}
			if err := tc.run(newR(f)); err != nil {
				t.Fatal(err)
			}
			want := slices.Concat(orDefault(tc.options, global), tc.args)
			got := f.args()
			if tc.name == "restore" {
				got = got[:len(got)-1] // the temp dir
			}
			if !slices.Equal(got, want) {
				t.Errorf("args =\n%q\nwant\n%q", got, want)
			}
			if got, want := f.env(), sorted(orDefault(tc.env, env)); !slices.Equal(got, want) {
				t.Errorf("env =\n%q\nwant\n%q", got, want)
			}
		})
	}
}

// orDefault returns a unless it is nil, else b.
func orDefault(a, b []string) []string {
	if a != nil {
		return a
	}
	return b
}

func TestResticCacheDirAndRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFake(t)
	r := &Restic{Binary: f.bin, Repo: "/repo", CacheDir: "/cache"}
	if err := r.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := f.args(), []string{"-r", "/repo", "--cache-dir", "/cache", "unlock"}; !slices.Equal(got, want) {
		t.Errorf("args = %q, want %q", got, want)
	}

	if err := r.Forget(ctx, Retention{}, nil, true); err == nil {
		t.Error("Forget with an empty retention ran")
	}
	if _, err := r.Snapshots(ctx, []string{"a,b"}); err == nil {
		t.Error("Snapshots took a tag with a comma")
	}
	if _, err := r.Backup(ctx, BackupOptions{}); err == nil {
		t.Error("Backup without paths ran")
	}
	full := t.TempDir()
	if err := os.WriteFile(filepath.Join(full, "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Restore(ctx, "latest", full); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("Restore into a full folder: %v", err)
	}
	if err := r.Restore(ctx, "latest", filepath.Join(full, "missing")); err == nil {
		t.Error("Restore into a missing folder ran")
	}
	if err := r.Restore(ctx, "--target", t.TempDir()); err == nil {
		t.Error("Restore took a flag for a snapshot id")
	}
	// A clash is found before restic runs.
	f.set("code", "1")
	clash := &Restic{Repo: "/src", Env: []string{"HOME=/other"}}
	r.Env = []string{"HOME=/home"}
	if err := r.Copy(ctx, clash); err == nil || !strings.Contains(err.Error(), "HOME") {
		t.Errorf("Copy with clashing HOME: %v", err)
	}
	if err := r.Init(ctx, clash); err == nil || !strings.Contains(err.Error(), "HOME") {
		t.Errorf("Init with clashing HOME: %v", err)
	}
}

func TestResticErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	secretEnv := []string{
		"RESTIC_PASSWORD=pw-value-1", "B2_ACCOUNT_ID=acct-value-2", "B2_ACCOUNT_KEY=key-value-3",
		"AWS_SESSION_TOKEN=token-value-4", "PATH=/bin",
	}
	stderr := "Fatal: pw-value-1 acct-value-2 key-value-3 token-value-4 extra-value-5 src-value-6 url-value-7\n"

	for _, tc := range []struct {
		code     string
		sentinel error
	}{{"1", nil}, {"10", ErrRepoMissing}, {"11", ErrLocked}, {"12", ErrWrongPassword}} {
		t.Run("exit "+tc.code, func(t *testing.T) {
			t.Parallel()
			f := newFake(t)
			f.set("stderr", stderr)
			f.set("code", tc.code)
			r := &Restic{Binary: f.bin, Repo: "rest:https://u:url-value-7@host/x", Env: secretEnv, Secrets: []string{"extra-value-5"}}
			from := &Restic{Repo: "/src", Env: []string{"RESTIC_PASSWORD=src-value-6"}}
			// Check knows nothing of from, whose password it never had.
			for i, err := range []error{r.Check(ctx, ""), r.Copy(ctx, from)} {
				var re *ResticError
				if !errors.As(err, &re) {
					t.Fatalf("error %v is not a *ResticError", err)
				}
				if re.ExitCode == 0 || !strings.Contains(err.Error(), "***") {
					t.Errorf("error = %v", err)
				}
				for _, v := range []string{"value-1", "value-2", "value-3", "value-4", "value-5", "value-6", "value-7"} {
					if strings.Contains(err.Error(), v) && (i == 1 || v != "value-6") {
						t.Errorf("error %q shows the secret %s", err, v)
					}
				}
				if tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
					t.Errorf("error %v does not match %v", err, tc.sentinel)
				}
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Errorf("error %v does not wrap the *exec.ExitError", err)
				}
			}
			ok, err := r.Exists(ctx)
			switch tc.code {
			case "10":
				if ok || err != nil {
					t.Errorf("Exists on exit 10 = %v, %v; want false, nil", ok, err)
				}
			default:
				if ok || err == nil {
					t.Errorf("Exists on exit %s = %v, %v; want an error", tc.code, ok, err)
				}
			}
		})
	}

	t.Run("incomplete backup", func(t *testing.T) {
		t.Parallel()
		f := newFake(t)
		f.set("stdout", `{"message_type":"summary","snapshot_id":"abc","files_new":1}`+"\n")
		f.set("code", "3")
		r := &Restic{Binary: f.bin, Repo: "/repo"}
		s, err := r.Backup(ctx, BackupOptions{Paths: []string{"/x"}})
		if !errors.Is(err, ErrIncomplete) || s.SnapshotID != "abc" {
			t.Errorf("Backup = %+v, %v; want the summary and ErrIncomplete", s, err)
		}
		if err := r.Check(ctx, ""); errors.Is(err, ErrIncomplete) {
			t.Errorf("exit 3 of check matches ErrIncomplete: %v", err)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		t.Parallel()
		f := newFake(t)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		r := &Restic{Binary: f.bin, Repo: "/repo"}
		if err := r.Unlock(cctx); !errors.Is(err, context.Canceled) {
			t.Errorf("Unlock on a cancelled context = %v", err)
		}
	})
}

// realRestic returns the path of restic, or skips the test.
func realRestic(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("runs restic")
	}
	bin, err := exec.LookPath("restic")
	if err != nil {
		if _, serr := os.Stat("/opt/homebrew/bin/restic"); serr != nil {
			t.Skip("restic is not installed")
		}
		bin = "/opt/homebrew/bin/restic"
	}
	return bin
}

func localRepo(t *testing.T, bin, password string) *Restic {
	t.Helper()
	home := t.TempDir()
	return &Restic{
		Binary: bin,
		Repo:   filepath.Join(t.TempDir(), "repo"),
		Env:    []string{"RESTIC_PASSWORD=" + password, "HOME=" + home, "TMPDIR=" + home, "PATH=/usr/bin:/bin"},
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResticReal(t *testing.T) {
	t.Parallel()
	bin := realRestic(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r := localRepo(t, bin, "first-password")

	if ok, err := r.Exists(ctx); ok || err != nil {
		t.Fatalf("Exists before Init = %v, %v", ok, err)
	}
	if err := r.Init(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.Exists(ctx); !ok || err != nil {
		t.Fatalf("Exists after Init = %v, %v", ok, err)
	}
	wrong := *r
	wrong.Env = slices.Clone(r.Env)
	wrong.Env[0] = "RESTIC_PASSWORD=not-the-password"
	if ok, err := wrong.Exists(ctx); ok || !errors.Is(err, ErrWrongPassword) || strings.Contains(err.Error(), "not-the-password") {
		t.Fatalf("Exists with a wrong password = %v, %v", ok, err)
	}

	data := t.TempDir()
	writeFile(t, filepath.Join(data, "a.txt"), "alpha")
	writeFile(t, filepath.Join(data, "sub", "b.txt"), "beta")
	writeFile(t, filepath.Join(data, "skip.tmp"), "skipped")
	s1, err := r.Backup(ctx, BackupOptions{Paths: []string{data}, Tags: []string{"core", "hourly"}, Excludes: []string{"*.tmp"}, Host: "h1"})
	if err != nil {
		t.Fatal(err)
	}
	if s1.FilesNew != 2 || s1.TotalFilesProcessed != 2 || s1.TotalBytesProcessed != 9 || len(s1.SnapshotID) != 64 || s1.DataAdded == 0 {
		t.Errorf("first summary = %+v", s1)
	}

	// A path that is not there: restic makes the snapshot and exits 3.
	other, err := r.Backup(ctx, BackupOptions{Paths: []string{data, filepath.Join(data, "missing")}, Tags: []string{"other"}, Host: "h1"})
	if !errors.Is(err, ErrIncomplete) || other.SnapshotID == "" {
		t.Errorf("backup of a missing path = %+v, %v; want a snapshot and ErrIncomplete", other, err)
	}

	writeFile(t, filepath.Join(data, "a.txt"), "alpha two")
	s2, err := r.Backup(ctx, BackupOptions{Paths: []string{data}, Tags: []string{"core", "hourly"}, Excludes: []string{"*.tmp"}, Host: "h1"})
	if err != nil {
		t.Fatal(err)
	}
	if s2.FilesChanged != 1 || s2.FilesUnmodified != 1 {
		t.Errorf("second summary = %+v", s2)
	}

	all, err := r.Snapshots(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].ID != s1.SnapshotID || all[2].ID != s2.SnapshotID {
		t.Fatalf("snapshots = %+v, want 3, oldest first", all)
	}
	if all[0].Summary == nil || all[0].Summary.TotalFilesProcessed != 2 || all[0].Hostname != "h1" || all[0].ShortID == "" {
		t.Errorf("first snapshot = %+v", all[0])
	}
	core, err := r.Snapshots(ctx, []string{"core", "hourly"})
	if err != nil || len(core) != 2 {
		t.Fatalf("core snapshots = %d, %v; want 2", len(core), err)
	}

	// keep-hourly 1 keeps the latest of the latest hour, whatever the
	// time: of the two core snapshots, the second.
	if err := r.Forget(ctx, Retention{Hourly: 1}, []string{"core"}, true); err != nil {
		t.Fatal(err)
	}
	core, err = r.Snapshots(ctx, []string{"core"})
	if err != nil || len(core) != 1 || core[0].ID != s2.SnapshotID {
		t.Fatalf("core snapshots after Forget = %+v, %v; want only the second", core, err)
	}
	if rest, _ := r.Snapshots(ctx, []string{"other"}); len(rest) != 1 {
		t.Errorf("Forget of core touched the other snapshot: %+v", rest)
	}

	if err := r.Check(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.Check(ctx, "5%"); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	if err := r.Restore(ctx, core[0].ID, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, core[0].Paths[0], "a.txt"))
	if err != nil || string(got) != "alpha two" {
		t.Errorf("restored a.txt = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(target, core[0].Paths[0], "skip.tmp")); err == nil {
		t.Error("the excluded file was restored")
	}
	if err := r.Restore(ctx, core[0].ID, target); err == nil {
		t.Error("Restore into a non-empty folder ran")
	}

	// A second repository with its own password and the first one's
	// chunker parameters; Copy brings every snapshot over.
	r2 := localRepo(t, bin, "second-password")
	r2.Env = slices.Clone(r.Env)
	r2.Env[0] = "RESTIC_PASSWORD=second-password"
	if err := r2.Init(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := r2.Copy(ctx, r); err != nil {
		t.Fatal(err)
	}
	copied, err := r2.Snapshots(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := r.Snapshots(ctx, nil); len(copied) != len(want) {
		t.Errorf("copied %d snapshots, want %d", len(copied), len(want))
	}
	if err := r2.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
}
