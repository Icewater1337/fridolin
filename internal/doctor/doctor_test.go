package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
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
}

// testNow is the time of every doctor test.
var testNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

type fakeRun struct {
	out string
	err error
}

func (s *fakeSystem) doctorEnv(cfg *config.Settings, cfgErr error) *Env {
	return &Env{
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
		Glob: func(pattern string) ([]string, error) {
			var out []string
			for p := range s.contents {
				if ok, _ := filepath.Match(pattern, p); ok {
					out = append(out, p)
				}
			}
			return out, nil
		},
		FreeBytes:  func(string) (uint64, error) { return s.free, s.freeErr },
		WriteProbe: func(string) error { return s.writeErr },
		Now:        func() time.Time { return testNow },
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

func testConfig(mut func(*config.Settings)) *config.Settings {
	cfg := &config.Settings{}
	cfg.ClaudePath, cfg.CodexPath, cfg.AgentProvider, cfg.DataDir = "claude", "codex", "claude", "/home/you/.fridolin"
	if mut != nil {
		mut(cfg)
	}
	return cfg
}

// testLoginVars stand in for the bridge's brainLoginVars.
var testLoginVars = []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}

// allChecks are this package's checks in a registry, as the bridge's has
// them.
func allChecks() []Check {
	var r Registry
	r.Add(SetupChecks(testLoginVars)...)
	r.Add(OwnerChecks()...)
	return r.Checks()
}

// result returns the report's line for the check id, failing the test
// when there is none.
func result(t *testing.T, rep Report, id string) CheckReport {
	t.Helper()
	for _, c := range rep.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no %s check in %+v", id, rep.Checks)
	return CheckReport{}
}

func TestChecks(t *testing.T) {
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
		{name: "everything there", id: "claude", status: OK, detail: "2.1.302", ok: true},
		{name: "config does not load", cfgErr: errors.New("queue.mode: unknown"), id: "config", status: Fail, detail: "queue.mode", fix: "fridolin check", ok: false},
		{name: "checks needing the config", cfgErr: errors.New("bad"), id: "data-dir", status: Warn, detail: "not checked", ok: false},
		{name: "claude missing", system: func(s *fakeSystem) { delete(s.path, "claude") }, id: "claude", status: Fail, detail: "not found", fix: "install.sh", ok: false},
		{name: "claude elsewhere", system: func(s *fakeSystem) { delete(s.path, "claude") }, config: func(c *config.Settings) { c.ClaudePath = "/opt/claude" }, id: "claude", status: Fail, fix: "claude_path", ok: false},
		{name: "claude old", system: func(s *fakeSystem) { s.runs["/usr/local/bin/claude --version"] = fakeRun{out: "2.1.100 (Claude Code)"} }, id: "claude", status: Warn, detail: "older than 2.1.302", fix: "claude update", ok: true},
		{name: "claude mute", system: func(s *fakeSystem) { s.runs["/usr/local/bin/claude --version"] = fakeRun{err: fakeExit(1)} }, id: "claude", status: Warn, detail: "no answer", ok: true},
		{name: "sandbox off", id: "claude-login", status: OK, detail: "sandbox is off", ok: true},
		{name: "sandbox on, no token", config: func(c *config.Settings) { c.Sandbox.Enabled = true }, id: "claude-login", status: Fail, detail: "Not logged in", fix: "claude setup-token", ok: false},
		{name: "sandbox on, token in shell", system: func(s *fakeSystem) { s.env["CLAUDE_CODE_OAUTH_TOKEN"] = "x" }, config: func(c *config.Settings) { c.Sandbox.Enabled = true }, id: "claude-login", status: OK, detail: "in this shell", ok: true},
		{name: "sandbox on, token in launchd agent", system: func(s *fakeSystem) {
			s.contents = map[string]string{"/home/you/Library/LaunchAgents/ai.fridolin.bridge.plist": "<dict><key>CLAUDE_CODE_OAUTH_TOKEN</key><string>x</string></dict>"}
		}, config: func(c *config.Settings) { c.Sandbox.Enabled = true }, id: "claude-login", status: OK, detail: "ai.fridolin.bridge.plist", ok: true},
		{name: "sandbox on, token in a systemd drop-in", system: func(s *fakeSystem) {
			s.goos = "linux"
			s.contents = map[string]string{"/home/you/.config/systemd/user/fridolin.service.d/token.conf": "[Service]\nEnvironment=\"ANTHROPIC_API_KEY=x\"\n"}
		}, config: func(c *config.Settings) { c.Sandbox.Enabled = true }, id: "claude-login", status: OK, detail: "fridolin.service.d/token.conf", ok: true},
		{name: "codex mode", config: func(c *config.Settings) { c.AgentProvider = "codex" }, id: "codex", status: OK, detail: "0.163.0", ok: true},
		{name: "codex old", system: func(s *fakeSystem) { s.runs["/usr/local/bin/codex --version"] = fakeRun{out: "codex-cli 0.150.2"} }, config: func(c *config.Settings) { c.AgentProvider = "codex" }, id: "codex", status: Warn, detail: "older than 0.163.0", ok: true},
		{name: "codex mute", system: func(s *fakeSystem) { s.runs["/usr/local/bin/codex --version"] = fakeRun{err: fakeExit(1)} }, config: func(c *config.Settings) { c.AgentProvider = "codex" }, id: "codex", status: Warn, detail: "no answer", ok: true},
		{name: "codex missing", system: func(s *fakeSystem) { delete(s.path, "codex") }, config: func(c *config.Settings) { c.AgentProvider = "codex" }, id: "codex", status: Fail, ok: false},
		{name: "agent by a bare name", id: "agent-path", status: OK, detail: `claude_path "claude" runs as /usr/local/bin/claude`, ok: true},
		{name: "codex by a bare name", config: func(c *config.Settings) { c.AgentProvider = "codex" }, id: "agent-path", status: OK, detail: `codex_path "codex" runs as /usr/local/bin/codex`, ok: true},
		{name: "agent by its path", system: func(s *fakeSystem) { s.path["/opt/claude"] = "/opt/claude" }, config: func(c *config.Settings) { c.ClaudePath = "/opt/claude" }, id: "agent-path", status: OK, detail: "claude_path /opt/claude", ok: true},
		{name: "codex not on PATH", system: func(s *fakeSystem) { delete(s.path, "codex") }, config: func(c *config.Settings) { c.AgentProvider = "codex" }, id: "agent-path", status: Fail, detail: `codex_path "codex" is no executable on PATH`, fix: "set codex_path in /home/you/.fridolin/config.json to the CLI's absolute path", ok: false},
		{name: "agent on a relative PATH entry", system: func(s *fakeSystem) { s.path["claude"] = "bin/claude" }, id: "agent-path", status: Fail, detail: "not an absolute path", fix: "set claude_path", ok: false},
		{name: "data dir not made yet", config: func(c *config.Settings) { c.DataDir = "/home/you/new/data" }, id: "data-dir", status: OK, detail: "creates it in /home/you", ok: true},
		{name: "data dir read-only", system: func(s *fakeSystem) { s.writeErr = fs.ErrPermission }, id: "data-dir", status: Fail, fix: "chmod u+w /home/you/.fridolin", ok: false},
		{name: "data dir not made yet, and read-only", system: func(s *fakeSystem) { s.writeErr = fs.ErrPermission }, config: func(c *config.Settings) { c.DataDir = "/home/you/new/data" }, id: "data-dir", status: Fail, fix: "mkdir -p /home/you/new/data", ok: false},
		{name: "data dir on no folder at all", system: func(s *fakeSystem) { s.dirs = map[string]bool{} }, id: "data-dir", status: OK, detail: "creates it in /", ok: true},
		{name: "disk low", system: func(s *fakeSystem) { s.free = 10 << 30 }, id: "disk", status: Warn, detail: "10.0 GB free", ok: true},
		{name: "disk full", system: func(s *fakeSystem) { s.free = 1 << 30 }, id: "disk", status: Fail, detail: "under 2.0 GB", ok: false},
		{name: "disk unknown", system: func(s *fakeSystem) { s.freeErr = errors.New("statfs: nope") }, id: "disk", status: Warn, detail: "statfs: nope", ok: true},
		{name: "tailscale missing", system: func(s *fakeSystem) { delete(s.path, "tailscale") }, id: "tailscale", status: Missing, fix: "brew install --cask tailscale", ok: true},
		{name: "tailscale missing on linux", system: func(s *fakeSystem) { s.goos = "linux"; delete(s.path, "tailscale") }, id: "tailscale", status: Missing, fix: "tailscale.com/install.sh", ok: true},
		{name: "tailscale missing on windows", system: func(s *fakeSystem) { s.goos = "windows"; delete(s.path, "tailscale") }, id: "tailscale", status: Missing, fix: "winget install --id Tailscale.Tailscale", ok: true},
		{name: "tailscale app CLI", system: func(s *fakeSystem) {
			delete(s.path, "tailscale")
			s.files = map[string]fs.FileMode{tailscaleAppCLI: 0o755}
			s.runs[tailscaleAppCLI+" status --json"] = fakeRun{out: `{"BackendState":"Running"}`}
		}, id: "tailscale", status: OK, detail: "Tailscale.app", ok: true},
		{name: "tailscale stopped", system: func(s *fakeSystem) {
			s.runs["/usr/local/bin/tailscale status --json"] = fakeRun{out: `{"BackendState":"Stopped"}`, err: fakeExit(1)}
		}, id: "tailscale", status: Warn, detail: "Stopped", fix: "tailscale up", ok: true},
		{name: "tailscale garbled", system: func(s *fakeSystem) { s.runs["/usr/local/bin/tailscale status --json"] = fakeRun{err: fakeExit(1)} }, id: "tailscale", status: Warn, detail: "failed", ok: true},
		{name: "tailscale says nothing", system: func(s *fakeSystem) { s.runs["/usr/local/bin/tailscale status --json"] = fakeRun{out: "{}"} }, id: "tailscale", status: Warn, detail: "gave no state", ok: true},
		{name: "auth key missing", system: func(s *fakeSystem) {
			s.runs["/usr/bin/security find-generic-password -a fridolin -s fridolin.tailscale.authkey"] = notFound
		}, id: "tailscale-authkey", status: Missing, detail: "the Keychain", fix: "security add-generic-password -a fridolin -s fridolin.tailscale.authkey -w", ok: true},
		{name: "keychain broken", system: func(s *fakeSystem) {
			s.runs["/usr/bin/security find-generic-password -a fridolin -s fridolin.tailscale.authkey"] = fakeRun{err: fakeExit(51)}
		}, id: "tailscale-authkey", status: Warn, detail: "cannot look", fix: "security unlock-keychain", ok: true},
		{name: "two B2 keys missing", system: func(s *fakeSystem) {
			s.runs["/usr/bin/security find-generic-password -a fridolin -s fridolin.b2.vault.key"] = notFound
			s.runs["/usr/bin/security find-generic-password -a fridolin -s fridolin.b2.snapshots.keyid"] = notFound
		}, id: "b2-keys", status: Missing, detail: "fridolin.b2.vault.key, fridolin.b2.snapshots.keyid", ok: true},
		{name: "linux secret file", system: func(s *fakeSystem) {
			s.goos = "linux"
			s.files = map[string]fs.FileMode{"/home/you/.config/fridolin/secrets/fridolin.tailscale.authkey": 0o600}
		}, id: "tailscale-authkey", status: OK, detail: "/home/you/.config/fridolin/secrets", ok: true},
		{name: "linux secret under XDG_CONFIG_HOME", system: func(s *fakeSystem) {
			s.goos = "linux"
			s.env["XDG_CONFIG_HOME"] = "/xdg"
		}, id: "tailscale-authkey", status: Missing, fix: "cat > /xdg/fridolin/secrets/fridolin.tailscale.authkey", ok: true},
		{name: "linux secret readable by others", system: func(s *fakeSystem) {
			s.goos = "linux"
			s.files = map[string]fs.FileMode{"/home/you/.config/fridolin/secrets/fridolin.tailscale.authkey": 0o644}
		}, id: "tailscale-authkey", status: Warn, fix: "chmod 600", ok: true},
		{name: "windows without the Credential Manager", system: func(s *fakeSystem) { s.goos = "windows" }, id: "tailscale-authkey", status: Warn, detail: "no access to the Windows Credential Manager", fix: "PowerShell", ok: true},
		{name: "docker missing", system: func(s *fakeSystem) { delete(s.path, "docker") }, id: "docker", status: Missing, fix: "orbstack", ok: true},
		{name: "docker missing on windows", system: func(s *fakeSystem) { s.goos = "windows"; delete(s.path, "docker") }, id: "docker", status: Missing, fix: "wsl --install", ok: true},
		{name: "docker daemon down", system: func(s *fakeSystem) {
			s.runs["/usr/local/bin/docker info --format {{.ServerVersion}}"] = fakeRun{err: fakeExit(1)}
		}, id: "docker", status: Warn, detail: "does not answer", fix: "OrbStack", ok: true},
		{name: "docker daemon down on linux", system: func(s *fakeSystem) {
			s.goos = "linux"
			s.runs["/usr/local/bin/docker info --format {{.ServerVersion}}"] = fakeRun{err: fakeExit(1)}
		}, id: "docker", status: Warn, fix: "systemctl start docker", ok: true},
		{name: "docker daemon down on windows", system: func(s *fakeSystem) {
			s.goos = "windows"
			s.runs["/usr/local/bin/docker info --format {{.ServerVersion}}"] = fakeRun{err: fakeExit(1)}
		}, id: "docker", status: Warn, fix: "Docker Desktop.exe", ok: true},
		{name: "restic", id: "restic", status: OK, detail: "restic 0.18.0", ok: true},
		{name: "restic mute", system: func(s *fakeSystem) { s.runs["/opt/homebrew/bin/restic version"] = fakeRun{err: fakeExit(1)} }, id: "restic", status: OK, detail: "/opt/homebrew/bin/restic", ok: true},
		{name: "restic missing on linux", system: func(s *fakeSystem) { s.goos = "linux"; delete(s.path, "restic") }, id: "restic", status: Missing, fix: "sudo apt install restic", ok: true},
		{name: "litestream missing on linux", system: func(s *fakeSystem) { s.goos = "linux"; delete(s.path, "litestream") }, id: "litestream", status: Missing, fix: "put it on PATH", ok: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := healthyMac()
			if tc.system != nil {
				tc.system(s)
			}
			var cfg *config.Settings
			if tc.cfgErr == nil {
				cfg = testConfig(tc.config)
			}
			rep := Run(context.Background(), s.doctorEnv(cfg, tc.cfgErr), allChecks())
			got := result(t, rep, tc.id)
			if got.Status != tc.status || !strings.Contains(got.Detail, tc.detail) {
				t.Errorf("%s: %s %q, want %s with %q", tc.id, got.Status, got.Detail, tc.status, tc.detail)
			}
			if tc.fix != "" && !strings.Contains(got.Fix, tc.fix) {
				t.Errorf("fix %q, want one with %q", got.Fix, tc.fix)
			}
			if got.Status == OK && got.Fix != "" {
				t.Errorf("an ok check has a fix: %q", got.Fix)
			}
			if rep.OK != tc.ok {
				t.Errorf("report OK %t, want %t", rep.OK, tc.ok)
			}
		})
	}
}

// The host's checks: on Windows the Credential Manager is asked whether
// a secret is there, and inside the core's container none of them runs.
func TestHostChecks(t *testing.T) {
	s := healthyMac()
	s.goos = "windows"
	env := s.doctorEnv(testConfig(nil), nil)
	env.HasHostSecret = func(name string) (bool, error) { return name != "fridolin.b2.vault.key", nil }
	rep := Run(context.Background(), env, allChecks())
	if c := result(t, rep, "tailscale-authkey"); c.Status != OK || !strings.Contains(c.Detail, "the Windows Credential Manager") {
		t.Errorf("windows, there: %+v", c)
	}
	if c := result(t, rep, "b2-keys"); c.Status != Missing || c.Fix != "fridolin core secrets set fridolin.b2.vault.key" {
		t.Errorf("windows, missing: %+v", c)
	}

	env = healthyMac().doctorEnv(testConfig(nil), nil)
	env.ConfigGOOS = config.ContainerGOOS
	for _, c := range Run(context.Background(), env, allChecks()).Checks {
		switch c.ID {
		case "tailscale", "tailscale-authkey", "b2-keys", "docker":
			t.Errorf("the %s check runs in the container", c.ID)
		}
	}

	// The checks other roles of a machine take on their own.
	rep = Run(context.Background(), healthyMac().doctorEnv(nil, nil), []Check{TailscaleCheck(true), DockerCheck()})
	if len(rep.Checks) != 2 || !rep.Checks[0].Required || rep.Checks[0].Status != OK || rep.Checks[1].Status != OK {
		t.Errorf("Tailscale and Docker on their own: %+v", rep.Checks)
	}
}

// Only the checks that concern the AI mode run, and the keychain is asked
// whether a secret is there, never for its value.
func TestAppliesAndNeverReadsSecrets(t *testing.T) {
	s := healthyMac()
	rep := Run(context.Background(), s.doctorEnv(testConfig(nil), nil), allChecks())
	for _, c := range rep.Checks {
		if c.ID == "codex" {
			t.Error("the codex check ran in Claude mode")
		}
	}
	codex := Run(context.Background(), s.doctorEnv(testConfig(func(c *config.Settings) { c.AgentProvider = "codex" }), nil), allChecks())
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
func TestOptionalChecksDoNotFail(t *testing.T) {
	s := healthyMac()
	s.path = map[string]string{"claude": "/usr/local/bin/claude"}
	s.runs = map[string]fakeRun{"/usr/local/bin/claude --version": {out: "2.1.302 (Claude Code)"}}
	rep := Run(context.Background(), s.doctorEnv(testConfig(nil), nil), allChecks())
	if !rep.OK {
		t.Errorf("a machine with only Claude fails: %+v", rep.Checks)
	}
	for _, c := range rep.Checks {
		if !c.Required && c.Status == Fail {
			t.Errorf("optional check %s fails", c.ID)
		}
		if c.Status != OK && c.Fix == "" {
			t.Errorf("%s is %s without a fix", c.ID, c.Status)
		}
	}
}

// A check that does not end in time ends with the context: doctor gives
// each one its own deadline.
func TestCheckDeadline(t *testing.T) {
	var deadline bool
	rep := Run(context.Background(), healthyMac().doctorEnv(testConfig(nil), nil), []Check{{
		ID: "slow", Title: "slow",
		Run: func(ctx context.Context, _ *Env) Result {
			_, deadline = ctx.Deadline()
			return Result{Status: OK}
		},
	}})
	if !deadline || len(rep.Checks) != 1 {
		t.Errorf("deadline %t, checks %+v", deadline, rep.Checks)
	}
}

// TestRegistry: a registry keeps its checks in the order they were added
// and refuses a second check with an ID it holds.
func TestRegistry(t *testing.T) {
	var r Registry
	r.Add(Check{ID: "a"}, Check{ID: "b"})
	r.Add(Check{ID: "c"})
	got := r.Checks()
	if len(got) != 3 || got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "c" {
		t.Fatalf("checks %+v", got)
	}
	got[0].ID = "changed"
	if r.Checks()[0].ID != "a" {
		t.Error("Checks hands out the registry's own slice")
	}
	defer func() {
		if p := recover(); p == nil || !strings.Contains(fmt.Sprint(p), "two checks with the ID b") {
			t.Errorf("a second check b: %v", p)
		}
	}()
	r.Add(Check{ID: "b"})
}

// TestWriteText: a line per check, the milestone of an optional check
// that is not ready, the fix below, and the count.
func TestWriteText(t *testing.T) {
	rep := Report{OK: true, Checks: []CheckReport{
		{ID: "a", Title: "a", Required: true, Status: OK, Detail: "fine"},
		{ID: "b", Title: "b", Needed: "M3", Status: Missing, Detail: "not there", Fix: "install b"},
		{ID: "c", Title: "c", Needed: "M1", Status: OK, Detail: "there"},
	}}
	var out bytes.Buffer
	if err := rep.WriteText(&out); err != nil {
		t.Fatal(err)
	}
	want := "ok       a: fine\n" +
		"missing  b (needed from M3): not there\n" +
		"         fix: install b\n" +
		"ok       c: there\n" +
		"\n2 ok, 0 warn, 1 missing, 0 fail; every required check passes\n"
	if out.String() != want {
		t.Errorf("text:\n%s\nwant:\n%s", out.String(), want)
	}
	rep.OK = false
	out.Reset()
	if err := rep.WriteText(&out); err != nil || !strings.HasSuffix(out.String(), "; a required check fails\n") {
		t.Errorf("failing: %v %q", err, out.String())
	}
	if err := rep.WriteText(failingWriter{}); err == nil {
		t.Error("a writer that fails: no error")
	}
}

type failingWriter struct{}

// captureLog sends what the standard logger writes to a buffer for the
// rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	return &buf
}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// Write loads the config file itself: one that does not load ends the
// command with ErrFailed and a JSON report that says so, and --redact
// writes ~ for the home folder in the paths of one that does, and in
// what is logged meanwhile.
func TestWrite(t *testing.T) {
	home := t.TempDir()
	bad := filepath.Join(home, "bad.json")
	good := filepath.Join(home, "good.json")
	if err := os.WriteFile(bad, []byte(`{"queue": {"mode": "sometimes"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, []byte(`{"data_dir": "~/data", "claude_path": "claude", "allowed_numbers": ["15551234567"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := config.Options{Paths: config.Paths{Home: home}, GOOS: "darwin", Getenv: func(string) string { return "" }}
	env := func(path string) *Env {
		e := healthyMac().doctorEnv(nil, nil)
		e.Home, e.ConfigPath = home, path
		e.LoadConfig = func(path string) (*config.Settings, error) { return config.LoadExisting(path, opts) }
		return e
	}

	var out bytes.Buffer
	err := Write(&out, Options{JSON: true}, env(bad), allChecks())
	if !errors.Is(err, ErrFailed) {
		t.Errorf("error %v, want ErrFailed", err)
	}
	var rep Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("%v in:\n%s", err, out.String())
	}
	if c := result(t, rep, "config"); rep.OK || c.Status != Fail || !strings.Contains(c.Detail, "sometimes") {
		t.Errorf("report %+v", rep)
	}

	out.Reset()
	e := env(good)
	logged := captureLog(t)
	checks := append(allChecks(), Check{ID: "logs", Title: "logs", Run: func(_ context.Context, env *Env) Result {
		log.Printf("looked at %s for 15551234567", env.Home)
		return Result{Status: OK, Detail: "15551234567 may write"}
	}})
	if err := Write(&out, Options{Redact: true}, e, checks); err != nil {
		t.Fatalf("%v:\n%s", err, out.String())
	}
	if strings.Contains(out.String(), home) || !strings.Contains(out.String(), "data dir writable: ~/data") || !strings.Contains(out.String(), "logs: 1…67 may write") {
		t.Errorf("not redacted:\n%s", out.String())
	}
	if got := logged.String(); strings.Contains(got, home) || strings.Contains(got, "15551234567") || !strings.Contains(got, "looked at ~ for 1…67") {
		t.Errorf("the log is not redacted: %q", got)
	}

	if err := Write(failingWriter{}, Options{}, env(good), allChecks()); err == nil || errors.Is(err, ErrFailed) {
		t.Errorf("a writer that fails: %v", err)
	}
}
