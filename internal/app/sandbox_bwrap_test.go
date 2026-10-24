package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/sandbox"
)

// bwrapProfiler is a profiler with the container's backend over the
// container's paths (/data, /opt/fridolin), with the bridge's programs
// fixed and what a path is decided by kinds instead of the disk, so the
// command line is the same on every machine.
func bwrapProfiler(t *testing.T, sb config.SandboxConfig, workDirs map[string][]string, kinds map[string]string) *SandboxProfiler {
	t.Helper()
	cfg := &config.Settings{
		Boot: config.Boot{DataDir: "/data", DBPath: "/data/bridge.db", MediaDir: "/data/media"},
		Live: config.Live{Sandbox: sb, ClaudePath: "/usr/local/bin/claude"},
	}
	cfg.Sandbox.Enabled = true
	sp := &SandboxProfiler{backend: backendBwrap, bwrapPath: sandbox.BwrapPath, home: "/var/lib/fridolin",
		bridgeExe: "/opt/fridolin/fridolin",
		kind:      func(p string) string { return kinds[p] },
		programs: func(sandboxConfig, string) sandbox.Programs {
			return sandbox.Programs{Files: []string{"/usr/local/bin/claude", "/usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe", "/usr/local/bin/rclone"}}
		},
		probe: func() error { return nil },
	}
	sp.Update(workDirs, cfg)
	sp.SetConfigFile("/data/config.json")
	return sp
}

// TestGoldenBwrapArgs pins bubblewrap's command line for a brain's run in
// the fridolin-core container, as the macOS profiles are pinned: the
// brain's own places writable, the system and its run's token folder
// read-only, and the masks where a bind would show what it must not see.
func TestGoldenBwrapArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		sb       config.SandboxConfig
		workDirs map[string][]string
		kinds    map[string]string
	}{
		{"bwrap-own-brain", config.SandboxConfig{}, map[string][]string{
			"main": {"/data/brains-folders/main"},
		}, nil},
		{"bwrap-other-brains", config.SandboxConfig{}, map[string][]string{
			"main":   {"/data/brains-folders/main", "/srv/main-link"},
			"family": {"/data/brains-folders/main/family"},
			"work":   {"/srv/work"},
		}, map[string]string{"/data/brains-folders/main/family": "dir"}},
		{"bwrap-deny-allow-paths", config.SandboxConfig{
			DenyPaths:      []string{"/data/brains-folders/main/private", "/data/brains-folders/main/notes.key", "/srv/clients", "/etc/secret.conf", "/data/brains-folders/main/not-there"},
			AllowPaths:     []string{"/srv/shared"},
			CredentialsDir: "/srv/shared/creds",
		}, map[string][]string{
			"main": {"/data/brains-folders/main"},
		}, map[string]string{
			"/data/brains-folders/main/private":   "dir",
			"/data/brains-folders/main/notes.key": "file",
			"/srv/clients":                        "dir",
			"/etc/secret.conf":                    "file",
			"/srv/shared/creds":                   "dir",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sp := bwrapProfiler(t, tc.sb, tc.workDirs, tc.kinds)
			got, err := sp.Profile("main")
			if err != nil {
				t.Fatal(err)
			}
			assertGolden(t, "sandbox/"+tc.name+".args", []byte(got))
		})
	}
}

// What the container's sandbox must hold whatever the settings: the
// namespaces, no bind of the data dir as a whole, and the other brains'
// folders, the database and the config never bound.
func TestBwrapArgsKeepTheDataDirOut(t *testing.T) {
	t.Parallel()
	sp := bwrapProfiler(t, config.SandboxConfig{}, map[string][]string{
		"main": {"/data/brains-folders/main"}, "work": {"/data/brains-folders/work"},
	}, nil)
	got, err := sp.Profile("main")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(got, "\n")
	for _, want := range []string{"--unshare-user", "--unshare-pid", "--die-with-parent", "--new-session"} {
		if !slices.Contains(lines, want) {
			t.Errorf("no %s in\n%s", want, got)
		}
	}
	for i, l := range lines {
		if l != "--bind" && l != "--ro-bind" && l != "--ro-bind-try" {
			continue
		}
		src := lines[i+1]
		if src == "/data" || src == "/" || strings.HasPrefix(src, "/data/brains-folders/work") ||
			src == "/data/bridge.db" || src == "/data/config.json" || strings.HasPrefix(src, "/data/secrets") {
			t.Errorf("%s %s binds what main must not see", l, src)
		}
	}
}

// The command writes the brain's passwd and group before the run, with
// the brain's own user and its state folder as home.
func TestBwrapCommandWritesTheBrainsUser(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	sp := bwrapProfiler(t, config.SandboxConfig{}, map[string][]string{"main": {filepath.Join(data, "brains-folders", "main")}}, nil)
	sp.DataDir = data
	sp.State = func(id string) sandbox.StatePaths { return sandbox.StatePaths{Dir: filepath.Join(data, "brains", id)} }
	// Available looks for bubblewrap, which this machine may not have.
	sp.bwrapPath = filepath.Join(data, "bwrap")
	if err := os.WriteFile(sp.bwrapPath, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	bin, args, err := sp.Command("main", "/usr/local/bin/claude", []string{"-p"})
	if err != nil {
		t.Fatal(err)
	}
	if bin != sp.bwrapPath || args[len(args)-2] != "/usr/local/bin/claude" || args[len(args)-1] != "-p" {
		t.Fatalf("command = %s %q", bin, args)
	}
	passwd, err := os.ReadFile(filepath.Join(data, "core", "sandbox", "main", "passwd"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(passwd), "brain-main:x:") || !strings.Contains(string(passwd), ":"+filepath.Join(data, "brains", "main")+":") {
		t.Errorf("passwd = %q", passwd)
	}
	if sandbox.BrainUID("main") == sandbox.BrainUID("family") && sandbox.BrainUID("main") == sandbox.BrainUID("work") {
		t.Error("every brain has the same uid")
	}
}

// A run under bubblewrap reaches what its tools need as it does under
// sandbox-exec: the vault's hydration copies in brains/<id>/tmp/vault
// (vault_get) writable, the run's --mcp-config and token read-only, the
// bridge's binary that the CLI starts as the MCP server (`fridolin mcp`,
// `fridolin memory search`), and the network, since the brain API they
// call is on the container's loopback. The memory indexes, the intents,
// the vault's index and documents and the audit log stay out of sight.
func TestBwrapRunReachesItsToolsAndNotTheirStores(t *testing.T) {
	t.Parallel()
	sp := bwrapProfiler(t, config.SandboxConfig{AllowPaths: []string{"/data"}}, map[string][]string{"main": {"/data/brains-folders/main"}}, map[string]string{
		"/data/memory": "dir", "/data/intents": "dir", "/data/vault": "dir", "/data/audit": "dir", "/data/vault.db": "file",
	})
	got, err := sp.Profile("main")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(got, "\n")
	bound := func(flag, path string) bool {
		for i, l := range lines[:len(lines)-1] {
			if l == flag && (lines[i+1] == path || strings.HasPrefix(path, strings.TrimSuffix(lines[i+1], "/")+"/")) {
				return true
			}
		}
		return false
	}
	for _, c := range []struct{ flag, path string }{
		{"--bind", "/data/brains/main/tmp/vault/documents/report.pdf"},
		{"--bind", "/data/brains-folders/main/notes.md"},
		{"--bind", "/data/media/main/photo.jpg"},
		{"--ro-bind-try", "/data/run-tokens/main/run-r1.mcp.json"},
		{"--ro-bind-try", "/opt/fridolin/fridolin"},
	} {
		if !bound(c.flag, c.path) {
			t.Errorf("%s is not under a %s in\n%s", c.path, c.flag, got)
		}
	}
	if slices.Contains(lines, "--unshare-net") || slices.Contains(lines, "--unshare-all") {
		t.Errorf("the run has no network, so no brain API:\n%s", got)
	}
	for _, m := range []string{"/data/memory", "/data/intents", "/data/vault", "/data/audit"} {
		if !strings.Contains(got, "--tmpfs\n"+m+"\n--remount-ro\n"+m+"\n") {
			t.Errorf("%s is not masked under allow_paths /data:\n%s", m, got)
		}
	}
	if !strings.Contains(got, "--ro-bind\n/dev/null\n/data/vault.db\n") {
		t.Errorf("vault.db is not masked:\n%s", got)
	}
}
