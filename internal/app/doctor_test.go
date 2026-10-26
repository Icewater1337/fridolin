package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/doctor"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/memindex"
)

// fakeExit is a program that ran and exited with code, as *exec.ExitError.
type fakeExit int

func (e fakeExit) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e fakeExit) ExitCode() int { return int(e) }

// fakeInfo is a file or folder for the fake Stat.
type fakeInfo struct {
	name string
	dir  bool
	mode fs.FileMode
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.dir }
func (f fakeInfo) Sys() any           { return nil }

// fakeSystem is the machine a doctor test runs on: the programs on PATH,
// what each command prints (keyed by the command line), the files and
// folders there are, and the free space.
type fakeSystem struct {
	goos     string
	env      map[string]string
	path     map[string]string // name → resolved path
	runs     map[string]fakeRun
	dirs     map[string]bool
	files    map[string]fs.FileMode
	contents map[string]string
	free     uint64
	freeErr  error
	writeErr error
	ran      []string
	// listening are the addresses Dial reaches.
	listening map[string]bool
}

// doctorNow is the time of every doctor test.
var doctorNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

type fakeRun struct {
	out string
	err error
}

func (s *fakeSystem) doctorEnv(cfg *config.Settings, cfgErr error) *doctor.Env {
	return &doctor.Env{
		GOOS: s.goos, Home: "/home/you", ConfigPath: "/home/you/.fridolin/config.json",
		Config: cfg, ConfigErr: cfgErr,
		Getenv: func(name string) string { return s.env[name] },
		LookPath: func(file string) (string, error) {
			if p, ok := s.path[file]; ok {
				return p, nil
			}
			return "", errors.New("executable file not found in $PATH")
		},
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			line := strings.Join(append([]string{name}, args...), " ")
			s.ran = append(s.ran, line)
			r, ok := s.runs[line]
			if !ok {
				return nil, fakeExit(127)
			}
			return []byte(r.out), r.err
		},
		Stat: func(p string) (fs.FileInfo, error) {
			if s.dirs[p] {
				return fakeInfo{name: filepath.Base(p), dir: true, mode: fs.ModeDir | 0o755}, nil
			}
			if m, ok := s.files[p]; ok {
				return fakeInfo{name: filepath.Base(p), mode: m}, nil
			}
			return nil, fs.ErrNotExist
		},
		ReadFile: func(p string) ([]byte, error) {
			if c, ok := s.contents[p]; ok {
				return []byte(c), nil
			}
			return nil, fs.ErrNotExist
		},
		Glob:       func(string) ([]string, error) { return nil, nil },
		FreeBytes:  func(string) (uint64, error) { return s.free, s.freeErr },
		WriteProbe: func(string) error { return s.writeErr },
		Dial: func(_ context.Context, addr string) error {
			if s.listening[addr] {
				return nil
			}
			return errors.New("connection refused")
		},
		Now: func() time.Time { return doctorNow },
	}
}

// healthyMac is a Mac with everything doctor looks for.
func healthyMac() *fakeSystem {
	s := &fakeSystem{
		goos: "darwin",
		env:  map[string]string{},
		path: map[string]string{
			"claude": "/usr/local/bin/claude", "tailscale": "/usr/local/bin/tailscale",
			"docker": "/usr/local/bin/docker", "restic": "/opt/homebrew/bin/restic",
			"rclone": "/opt/homebrew/bin/rclone", "ffmpeg": "/opt/homebrew/bin/ffmpeg",
			"litestream": "/opt/homebrew/bin/litestream", "age": "/opt/homebrew/bin/age",
			"codex": "/usr/local/bin/codex",
		},
		runs: map[string]fakeRun{
			"/usr/local/bin/claude --version":                        {out: "2.1.302 (Claude Code)\n"},
			"/usr/local/bin/codex --version":                         {out: "codex-cli 0.163.0\n"},
			"/usr/local/bin/tailscale status --json":                 {out: `{"Version":"1.90.0","BackendState":"Running"}`},
			"/usr/local/bin/docker info --format {{.ServerVersion}}": {out: "28.1.1\n"},
			"/opt/homebrew/bin/restic version":                       {out: "restic 0.18.0 compiled with go1.24\n"},
			"/opt/homebrew/bin/rclone version":                       {out: "rclone v1.70.0\n- os/version: darwin\n"},
			"/opt/homebrew/bin/ffmpeg -version":                      {out: "ffmpeg version 7.1\n"},
			"/opt/homebrew/bin/litestream version":                   {out: "v0.3.13\n"},
			"/opt/homebrew/bin/age --version":                        {out: "v1.2.1\n"},
		},
		dirs: map[string]bool{"/": true, "/home": true, "/home/you": true, "/home/you/.fridolin": true},
		free: 100 << 30,
	}
	for _, name := range []string{hostsecrets.TailscaleAuthKey, hostsecrets.B2VaultKeyID, hostsecrets.B2VaultKey, hostsecrets.B2SnapshotsKeyID, hostsecrets.B2SnapshotsKey} {
		s.runs["/usr/bin/security find-generic-password -a fridolin -s "+name] = fakeRun{out: "keychain: \"login.keychain-db\"\n"}
	}
	return s
}

func doctorConfig(mut func(*config.Settings)) *config.Settings {
	cfg := &config.Settings{}
	cfg.ClaudePath, cfg.CodexPath, cfg.AgentProvider, cfg.DataDir = "claude", "codex", "claude", "/home/you/.fridolin"
	if mut != nil {
		mut(cfg)
	}
	return cfg
}

// result returns the report's line for the check id, failing the test
// when there is none.
func result(t *testing.T, rep doctor.Report, id string) doctor.CheckReport {
	t.Helper()
	for _, c := range rep.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no %s check in %+v", id, rep.Checks)
	return doctor.CheckReport{}
}

// A check a later milestone needs is optional now: it may be missing
// without failing the report, and says which milestone needs it.
func TestDoctorOptionalChecksDoNotFail(t *testing.T) {
	t.Parallel()
	s := healthyMac()
	s.path = map[string]string{"claude": "/usr/local/bin/claude"}
	s.runs = map[string]fakeRun{"/usr/local/bin/claude --version": {out: "2.1.302 (Claude Code)"}}
	rep := doctor.Run(context.Background(), s.doctorEnv(doctorConfig(nil), nil), DoctorRegistry.Checks())
	if !rep.OK {
		t.Errorf("a machine with only Claude fails: %+v", rep.Checks)
	}
	for _, c := range rep.Checks {
		if !c.Required && c.Status == doctor.Fail {
			t.Errorf("optional check %s fails", c.ID)
		}
		if c.Status != doctor.OK && c.Fix == "" {
			t.Errorf("%s is %s without a fix", c.ID, c.Status)
		}
	}
}

// TestGoldenDoctor pins what doctor prints, as text and as JSON, on a fake
// Mac that has the bridge's own needs and lacks most of what later
// milestones add.
func TestGoldenDoctor(t *testing.T) {
	t.Parallel()
	s := healthyMac()
	delete(s.path, "docker")
	delete(s.path, "litestream")
	s.runs["/usr/local/bin/tailscale status --json"] = fakeRun{out: `{"BackendState":"NeedsLogin"}`, err: fakeExit(1)}
	s.runs["/usr/bin/security find-generic-password -a fridolin -s fridolin.b2.snapshots.key"] = fakeRun{err: fakeExit(44)}
	s.runs["/usr/bin/security find-generic-password -a fridolin -s fridolin.b2.snapshots.keyid"] = fakeRun{err: fakeExit(44)}
	s.free = 12 << 30
	rep := doctor.Run(context.Background(), s.doctorEnv(doctorConfig(func(c *config.Settings) { c.Sandbox.Enabled = true }), nil), DoctorRegistry.Checks())
	var text bytes.Buffer
	if err := rep.WriteText(&text); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "cli/doctor-text.golden", text.Bytes())
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "cli/doctor-json.golden", append(data, '\n'))
}

// The memory index's check runs for the brains whose approved manifest
// uses the index: it fails when the provenance gate does, and warns about
// a brain without an index or with files changed since it was indexed.
func TestDoctorMemoryIndex(t *testing.T) {
	t.Parallel()
	if memoryEnabled(healthyMac().doctorEnv(nil, errors.New("bad"))) {
		t.Error("the memory check applies without a config")
	}
	cfg := doctorConfig(func(c *config.Settings) {
		c.BrainDirs = []config.BrainRef{
			{Path: "/home/you/Personal", Approved: &manifest.Manifest{ID: "personal", Memory: &manifest.Memory{Inject: true}}},
			{Path: "/home/you/Work", Approved: &manifest.Manifest{ID: "work"}},
			{Path: "/home/you/New"},
		}
	})
	env := healthyMac().doctorEnv(cfg, nil)
	if !memoryEnabled(env) || len(memoryBrains(env)) != 1 {
		t.Fatalf("memory brains %v", memoryBrains(env))
	}
	var stale []string
	var staleErr error
	env.MemoryStale = func(_ context.Context, b memindex.Brain) ([]string, error) {
		if b.ID != "personal" || b.Dir != "/home/you/Personal" {
			t.Errorf("asked about %+v", b)
		}
		return stale, staleErr
	}
	if r := checkMemoryIndex(context.Background(), env); r.Status != doctor.OK || r.Detail != "gate ok; personal: up to date" {
		t.Errorf("up to date: %+v", r)
	}
	stale = []string{"notes/a.md", "notes/b.md"}
	if r := checkMemoryIndex(context.Background(), env); r.Status != doctor.Warn || !strings.Contains(r.Detail, "personal: 2 files changed") || !strings.Contains(r.Fix, "reindex --brain personal") {
		t.Errorf("stale: %+v", r)
	}
	staleErr = errors.New("no such file")
	if r := checkMemoryIndex(context.Background(), env); r.Status != doctor.Warn || !strings.Contains(r.Detail, "personal: no index yet") {
		t.Errorf("no index: %+v", r)
	}
	env.MemorySelfTest = func(context.Context) error { return errors.New("recalled an untrusted fact") }
	if r := checkMemoryIndex(context.Background(), env); r.Status != doctor.Fail || !strings.Contains(r.Detail, "recalled an untrusted fact") {
		t.Errorf("gate failed: %+v", r)
	}
}

// The system's doctor loads the config file as the bridge would: one that
// does not load ends the command with doctor.ErrFailed and a report that
// says so, and the paths of one that does are resolved against the home
// folder.
func TestDoctorLoadsTheConfig(t *testing.T) {
	// Serial: fixtureHome sets the process's HOME, which the config's ~
	// is resolved against, and loading sets the process's PATH.
	_, home := fixtureHome(t)
	bad := filepath.Join(home, "bad.json")
	good := filepath.Join(home, "good.json")
	if err := os.WriteFile(bad, []byte(`{"queue": {"mode": "sometimes"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, []byte(`{"data_dir": "~/data", "claude_path": "claude"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(path string) *doctor.Env {
		e := healthyMac().doctorEnv(nil, nil)
		e.Home, e.ConfigPath, e.LoadConfig = home, path, loadDoctorConfig
		return e
	}
	checks := doctor.SetupChecks(brainLoginVars)[:2] // config, data-dir

	var out bytes.Buffer
	err := doctor.Write(&out, doctor.Options{JSON: true}, env(bad), checks)
	if !errors.Is(err, doctor.ErrFailed) {
		t.Errorf("error %v, want doctor.ErrFailed", err)
	}
	var rep doctor.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("%v in:\n%s", err, out.String())
	}
	if c := result(t, rep, "config"); rep.OK || c.Status != doctor.Fail || !strings.Contains(c.Detail, "sometimes") {
		t.Errorf("report %+v", rep)
	}

	out.Reset()
	if err := doctor.Write(&out, doctor.Options{}, env(good), checks); err != nil {
		t.Fatalf("%v:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "data dir writable: "+filepath.Join(home, "data")) {
		t.Errorf("the data dir is not resolved:\n%s", out.String())
	}
}
