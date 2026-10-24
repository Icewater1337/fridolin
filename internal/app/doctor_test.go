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

func (s *fakeSystem) doctorEnv(cfg *config.Settings, cfgErr error) *DoctorEnv {
	return &DoctorEnv{
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
	for _, name := range append([]string{tailscaleAuthKeySecret}, b2Secrets...) {
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
func result(t *testing.T, rep DoctorReport, id string) DoctorCheckReport {
	t.Helper()
	for _, c := range rep.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no %s check in %+v", id, rep.Checks)
	return DoctorCheckReport{}
}

func TestDoctorChecks(t *testing.T) {
	t.Parallel()
	notFound := fakeRun{err: fakeExit(44)}
	tests := []struct {
		name   string
		system func(s *fakeSystem)
		config func(cfg *config.Settings)
		cfgErr error
		id     string
		status string
		detail string // part of the detail
		fix    string // part of the fix; "" for none
		ok     bool   // the report's OK
	}{
		{name: "everything there", id: "claude", status: DoctorOK, detail: "2.1.302", ok: true},
		{name: "config does not load", cfgErr: errors.New("queue.mode: unknown"), id: "config", status: DoctorFail, detail: "queue.mode", fix: "fridolin check", ok: false},
		{name: "checks needing the config", cfgErr: errors.New("bad"), id: "data-dir", status: DoctorWarn, detail: "not checked", ok: false},
		{name: "claude missing", system: func(s *fakeSystem) { delete(s.path, "claude") }, id: "claude", status: DoctorFail, detail: "not found", fix: "install.sh", ok: false},
		{name: "claude elsewhere", system: func(s *fakeSystem) { delete(s.path, "claude") }, config: func(c *config.Settings) { c.ClaudePath = "/opt/claude" }, id: "claude", status: DoctorFail, fix: "claude_path", ok: false},
		{name: "claude old", system: func(s *fakeSystem) { s.runs["/usr/local/bin/claude --version"] = fakeRun{out: "2.1.100 (Claude Code)"} }, id: "claude", status: DoctorWarn, detail: "older than 2.1.302", fix: "claude update", ok: true},
		{name: "claude mute", system: func(s *fakeSystem) { s.runs["/usr/local/bin/claude --version"] = fakeRun{err: fakeExit(1)} }, id: "claude", status: DoctorWarn, detail: "no answer", ok: true},
		{name: "sandbox off", id: "claude-login", status: DoctorOK, detail: "sandbox is off", ok: true},
		{name: "sandbox on, no token", config: func(c *config.Settings) { c.Sandbox.Enabled = true }, id: "claude-login", status: DoctorFail, detail: "Not logged in", fix: "claude setup-token", ok: false},
		{name: "sandbox on, token in shell", system: func(s *fakeSystem) { s.env["CLAUDE_CODE_OAUTH_TOKEN"] = "x" }, config: func(c *config.Settings) { c.Sandbox.Enabled = true }, id: "claude-login", status: DoctorOK, detail: "in this shell", ok: true},
		{name: "sandbox on, token in launchd agent", system: func(s *fakeSystem) {
			s.contents = map[string]string{"/home/you/Library/LaunchAgents/ai.fridolin.bridge.plist": "<dict><key>CLAUDE_CODE_OAUTH_TOKEN</key><string>x</string></dict>"}
		}, config: func(c *config.Settings) { c.Sandbox.Enabled = true }, id: "claude-login", status: DoctorOK, detail: "ai.fridolin.bridge.plist", ok: true},
		{name: "codex mode", config: func(c *config.Settings) { c.AgentProvider = "codex" }, id: "codex", status: DoctorOK, detail: "0.163.0", ok: true},
		{name: "codex old", system: func(s *fakeSystem) { s.runs["/usr/local/bin/codex --version"] = fakeRun{out: "codex-cli 0.150.2"} }, config: func(c *config.Settings) { c.AgentProvider = "codex" }, id: "codex", status: DoctorWarn, detail: "older than 0.163.0", ok: true},
		{name: "codex missing", system: func(s *fakeSystem) { delete(s.path, "codex") }, config: func(c *config.Settings) { c.AgentProvider = "codex" }, id: "codex", status: DoctorFail, ok: false},
		{name: "agent by a bare name", id: "agent-path", status: DoctorOK, detail: `claude_path "claude" runs as /usr/local/bin/claude`, ok: true},
		{name: "codex by a bare name", config: func(c *config.Settings) { c.AgentProvider = "codex" }, id: "agent-path", status: DoctorOK, detail: `codex_path "codex" runs as /usr/local/bin/codex`, ok: true},
		{name: "agent by its path", system: func(s *fakeSystem) { s.path["/opt/claude"] = "/opt/claude" }, config: func(c *config.Settings) { c.ClaudePath = "/opt/claude" }, id: "agent-path", status: DoctorOK, detail: "claude_path /opt/claude", ok: true},
		{name: "codex not on PATH", system: func(s *fakeSystem) { delete(s.path, "codex") }, config: func(c *config.Settings) { c.AgentProvider = "codex" }, id: "agent-path", status: DoctorFail, detail: `codex_path "codex" is no executable on PATH`, fix: "set codex_path in /home/you/.fridolin/config.json to the CLI's absolute path", ok: false},
		{name: "agent on a relative PATH entry", system: func(s *fakeSystem) { s.path["claude"] = "bin/claude" }, id: "agent-path", status: DoctorFail, detail: "not an absolute path", fix: "set claude_path", ok: false},
		{name: "data dir not made yet", config: func(c *config.Settings) { c.DataDir = "/home/you/new/data" }, id: "data-dir", status: DoctorOK, detail: "creates it in /home/you", ok: true},
		{name: "data dir read-only", system: func(s *fakeSystem) { s.writeErr = fs.ErrPermission }, id: "data-dir", status: DoctorFail, fix: "chmod u+w /home/you/.fridolin", ok: false},
		{name: "disk low", system: func(s *fakeSystem) { s.free = 10 << 30 }, id: "disk", status: DoctorWarn, detail: "10.0 GB free", ok: true},
		{name: "disk full", system: func(s *fakeSystem) { s.free = 1 << 30 }, id: "disk", status: DoctorFail, detail: "under 2.0 GB", ok: false},
		{name: "disk unknown", system: func(s *fakeSystem) { s.freeErr = errors.New("statfs: nope") }, id: "disk", status: DoctorWarn, detail: "statfs: nope", ok: true},
		{name: "tailscale missing", system: func(s *fakeSystem) { delete(s.path, "tailscale") }, id: "tailscale", status: DoctorMissing, fix: "brew install --cask tailscale", ok: true},
		{name: "tailscale app CLI", system: func(s *fakeSystem) {
			delete(s.path, "tailscale")
			s.files = map[string]fs.FileMode{tailscaleAppCLI: 0o755}
			s.runs[tailscaleAppCLI+" status --json"] = fakeRun{out: `{"BackendState":"Running"}`}
		}, id: "tailscale", status: DoctorOK, detail: "Tailscale.app", ok: true},
		{name: "tailscale stopped", system: func(s *fakeSystem) {
			s.runs["/usr/local/bin/tailscale status --json"] = fakeRun{out: `{"BackendState":"Stopped"}`, err: fakeExit(1)}
		}, id: "tailscale", status: DoctorWarn, detail: "Stopped", fix: "tailscale up", ok: true},
		{name: "tailscale garbled", system: func(s *fakeSystem) { s.runs["/usr/local/bin/tailscale status --json"] = fakeRun{err: fakeExit(1)} }, id: "tailscale", status: DoctorWarn, detail: "failed", ok: true},
		{name: "auth key missing", system: func(s *fakeSystem) {
			s.runs["/usr/bin/security find-generic-password -a fridolin -s fridolin.tailscale.authkey"] = notFound
		}, id: "tailscale-authkey", status: DoctorMissing, detail: "the Keychain", fix: "security add-generic-password -a fridolin -s fridolin.tailscale.authkey -w", ok: true},
		{name: "keychain broken", system: func(s *fakeSystem) {
			s.runs["/usr/bin/security find-generic-password -a fridolin -s fridolin.tailscale.authkey"] = fakeRun{err: fakeExit(51)}
		}, id: "tailscale-authkey", status: DoctorWarn, detail: "cannot look", ok: true},
		{name: "two B2 keys missing", system: func(s *fakeSystem) {
			s.runs["/usr/bin/security find-generic-password -a fridolin -s fridolin.b2.vault.key"] = notFound
			s.runs["/usr/bin/security find-generic-password -a fridolin -s fridolin.b2.snapshots.keyid"] = notFound
		}, id: "b2-keys", status: DoctorMissing, detail: "fridolin.b2.vault.key, fridolin.b2.snapshots.keyid", ok: true},
		{name: "linux secret file", system: func(s *fakeSystem) {
			s.goos = "linux"
			s.files = map[string]fs.FileMode{"/home/you/.config/fridolin/secrets/fridolin.tailscale.authkey": 0o600}
		}, id: "tailscale-authkey", status: DoctorOK, detail: "/home/you/.config/fridolin/secrets", ok: true},
		{name: "linux secret under XDG_CONFIG_HOME", system: func(s *fakeSystem) {
			s.goos = "linux"
			s.env["XDG_CONFIG_HOME"] = "/xdg"
		}, id: "tailscale-authkey", status: DoctorMissing, fix: "cat > /xdg/fridolin/secrets/fridolin.tailscale.authkey", ok: true},
		{name: "linux secret readable by others", system: func(s *fakeSystem) {
			s.goos = "linux"
			s.files = map[string]fs.FileMode{"/home/you/.config/fridolin/secrets/fridolin.tailscale.authkey": 0o644}
		}, id: "tailscale-authkey", status: DoctorWarn, fix: "chmod 600", ok: true},
		{name: "docker missing", system: func(s *fakeSystem) { delete(s.path, "docker") }, id: "docker", status: DoctorMissing, fix: "orbstack", ok: true},
		{name: "docker daemon down", system: func(s *fakeSystem) {
			s.runs["/usr/local/bin/docker info --format {{.ServerVersion}}"] = fakeRun{err: fakeExit(1)}
		}, id: "docker", status: DoctorWarn, detail: "does not answer", ok: true},
		{name: "docker daemon down on linux", system: func(s *fakeSystem) {
			s.goos = "linux"
			s.runs["/usr/local/bin/docker info --format {{.ServerVersion}}"] = fakeRun{err: fakeExit(1)}
		}, id: "docker", status: DoctorWarn, fix: "systemctl start docker", ok: true},
		{name: "restic", id: "restic", status: DoctorOK, detail: "restic 0.18.0", ok: true},
		{name: "restic missing on linux", system: func(s *fakeSystem) { s.goos = "linux"; delete(s.path, "restic") }, id: "restic", status: DoctorMissing, fix: "sudo apt install restic", ok: true},
		{name: "litestream missing on linux", system: func(s *fakeSystem) { s.goos = "linux"; delete(s.path, "litestream") }, id: "litestream", status: DoctorMissing, fix: "put it on PATH", ok: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := healthyMac()
			if tc.system != nil {
				tc.system(s)
			}
			var cfg *config.Settings
			if tc.cfgErr == nil {
				cfg = doctorConfig(tc.config)
			}
			rep := RunDoctor(context.Background(), s.doctorEnv(cfg, tc.cfgErr), DoctorRegistry)
			got := result(t, rep, tc.id)
			if got.Status != tc.status || !strings.Contains(got.Detail, tc.detail) {
				t.Errorf("%s: %s %q, want %s with %q", tc.id, got.Status, got.Detail, tc.status, tc.detail)
			}
			if tc.fix != "" && !strings.Contains(got.Fix, tc.fix) {
				t.Errorf("fix %q, want one with %q", got.Fix, tc.fix)
			}
			if got.Status == DoctorOK && got.Fix != "" {
				t.Errorf("an ok check has a fix: %q", got.Fix)
			}
			if rep.OK != tc.ok {
				t.Errorf("report OK %t, want %t", rep.OK, tc.ok)
			}
		})
	}
}

// Only the checks that concern the AI mode run, and the keychain is asked
// whether a secret is there, never for its value.
func TestDoctorAppliesAndNeverReadsSecrets(t *testing.T) {
	t.Parallel()
	s := healthyMac()
	rep := RunDoctor(context.Background(), s.doctorEnv(doctorConfig(nil), nil), DoctorRegistry)
	for _, c := range rep.Checks {
		if c.ID == "codex" {
			t.Error("the codex check ran in Claude mode")
		}
	}
	codex := RunDoctor(context.Background(), s.doctorEnv(doctorConfig(func(c *config.Settings) { c.AgentProvider = "codex" }), nil), DoctorRegistry)
	for _, c := range codex.Checks {
		if c.ID == "claude" || c.ID == "claude-login" {
			t.Errorf("the %s check ran in Codex mode", c.ID)
		}
	}
	var asked int
	for _, line := range s.ran {
		if strings.HasPrefix(line, "/usr/bin/security") {
			asked++
			if strings.Contains(line, " -w") || strings.Contains(line, " -g") {
				t.Errorf("doctor read a secret's value: %s", line)
			}
		}
	}
	if asked != 2*(1+len(b2Secrets)) {
		t.Errorf("asked the keychain %d times, want %d", asked, 2*(1+len(b2Secrets)))
	}
}

// A check a later milestone needs is optional now: it may be missing
// without failing the report, and says which milestone needs it.
func TestDoctorOptionalChecksDoNotFail(t *testing.T) {
	t.Parallel()
	s := healthyMac()
	s.path = map[string]string{"claude": "/usr/local/bin/claude"}
	s.runs = map[string]fakeRun{"/usr/local/bin/claude --version": {out: "2.1.302 (Claude Code)"}}
	rep := RunDoctor(context.Background(), s.doctorEnv(doctorConfig(nil), nil), DoctorRegistry)
	if !rep.OK {
		t.Errorf("a machine with only Claude fails: %+v", rep.Checks)
	}
	for _, c := range rep.Checks {
		if !c.Required && c.Status == DoctorFail {
			t.Errorf("optional check %s fails", c.ID)
		}
		if c.Status != DoctorOK && c.Fix == "" {
			t.Errorf("%s is %s without a fix", c.ID, c.Status)
		}
	}
}

// A check that does not end in time ends with the context: doctor gives
// each one its own deadline.
func TestDoctorCheckDeadline(t *testing.T) {
	t.Parallel()
	var deadline bool
	rep := RunDoctor(context.Background(), healthyMac().doctorEnv(doctorConfig(nil), nil), []DoctorCheck{{
		ID: "slow", Title: "slow",
		Run: func(ctx context.Context, _ *DoctorEnv) DoctorResult {
			_, deadline = ctx.Deadline()
			return DoctorResult{Status: DoctorOK}
		},
	}})
	if !deadline || len(rep.Checks) != 1 {
		t.Errorf("deadline %t, checks %+v", deadline, rep.Checks)
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
	rep := RunDoctor(context.Background(), s.doctorEnv(doctorConfig(func(c *config.Settings) { c.Sandbox.Enabled = true }), nil), DoctorRegistry)
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

// writeDoctor loads the config file itself: one that does not load ends
// the command with ErrDoctorFailed and a JSON report that says so, and
// --redact writes ~ for the home folder in the paths of one that does.
func TestWriteDoctor(t *testing.T) {
	// Serial: doctor redirects the process's standard logger.
	_, home := fixtureHome(t)
	bad := filepath.Join(home, "bad.json")
	good := filepath.Join(home, "good.json")
	if err := os.WriteFile(bad, []byte(`{"queue": {"mode": "sometimes"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, []byte(`{"data_dir": "~/data", "claude_path": "claude"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(path string) *DoctorEnv {
		s := healthyMac()
		e := s.doctorEnv(nil, nil)
		e.Home, e.ConfigPath = home, path
		return e
	}

	var out bytes.Buffer
	err := writeDoctor(&out, DoctorOptions{JSON: true}, env(bad), DoctorRegistry)
	if !errors.Is(err, ErrDoctorFailed) {
		t.Errorf("error %v, want ErrDoctorFailed", err)
	}
	var rep DoctorReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("%v in:\n%s", err, out.String())
	}
	if c := result(t, rep, "config"); rep.OK || c.Status != DoctorFail || !strings.Contains(c.Detail, "sometimes") {
		t.Errorf("report %+v", rep)
	}

	out.Reset()
	if err := writeDoctor(&out, DoctorOptions{Redact: true}, env(good), DoctorRegistry); err != nil {
		t.Fatalf("%v:\n%s", err, out.String())
	}
	if strings.Contains(out.String(), home) || !strings.Contains(out.String(), "data dir writable: ~/data") {
		t.Errorf("not redacted:\n%s", out.String())
	}
}
