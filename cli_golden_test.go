package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// linkBridgeBinary links (or copies) the test binary to <root>/bin/fridolin.
// Run with FRIDOLIN_TEST_RUN_MAIN=1 it is the bridge's own main (see
// TestMain), and the sandbox profile names it as the bridge binary, so
// that the profile's paths all lie in the test's folder.
func linkBridgeBinary(t *testing.T, root string) string {
	t.Helper()
	bin := filepath.Join(root, "bin", "fridolin")
	if _, err := os.Stat(bin); err == nil {
		return bin
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(os.Args[0], bin); err == nil {
		return bin
	}
	src, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(bin, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	return bin
}

// runBridgeCLI runs `fridolin args...` from <root>/bin/fridolin, in root,
// with HOME at <root>/home and nothing else of the test's environment but
// PATH's system folders and the fake Claude CLI's switch. It returns what
// the command printed, stdout then stderr, and its exit code.
func runBridgeCLI(t *testing.T, root string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(linkBridgeBinary(t, root), args...)
	cmd.Dir = root
	cmd.Env = []string{
		"FRIDOLIN_TEST_RUN_MAIN=1",
		"FRIDOLIN_FAKE_CLAUDE=1",
		"HOME=" + filepath.Join(root, "home"),
		"PATH=/usr/bin:/bin",
		"GORACE=atexit_sleep_ms=0",
		// Where a binary built with -cover writes its counters; without it,
		// `go test -cover` would add a warning to what the command prints.
		"GOCOVERDIR=" + t.TempDir(),
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	return stdout.String() + stderr.String(), code
}

// cliFixture is a test folder for the command line: <root>/home with
// brains, and <root>/config.json, whose claude_path is the bridge binary
// playing the Claude CLI. config holds the settings besides those, as
// "key": value pairs; brains are made with brainFolder.
func cliFixture(t *testing.T, config string, brains func(home string) []BrainRef) (root, cfgPath string) {
	t.Helper()
	root, home := fixtureHome(t)
	var refs []BrainRef
	if brains != nil {
		refs = brains(home)
	}
	cfgPath = filepath.Join(root, "config.json")
	body := `{
		"claude_path": "` + filepath.Join(root, "bin", "fridolin") + `",
		"data_dir": "~/.fridolin",
		"allowed_numbers": ["15551234567"],
		"allowed_groups": ["120363000000000000@g.us"]` + brainDirsJSON(t, refs...)
	if config != "" {
		body += ", " + config
	}
	if err := os.WriteFile(cfgPath, []byte(body+"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkBridgeBinary(t, root)
	return root, cfgPath
}

// TestGoldenCheck pins what `fridolin check` prints: the summary, with
// --route the brain each chat goes to, and a config that does not load.
func TestGoldenCheck(t *testing.T) {
	brains := func(home string) []BrainRef {
		return []BrainRef{
			brainFolder(t, "~/main", BrainManifest{ID: "main", Name: "Main", Routing: BrainRouting{HandlesDMs: true}}),
			brainFolder(t, "~/family", BrainManifest{ID: "family", Name: "Family", Routing: BrainRouting{Groups: []string{harnessGroup}}}),
			// A folder whose brain.json was changed after it was approved.
			func() BrainRef {
				ref := brainFolder(t, "~/drifted", BrainManifest{ID: "drifted", Name: "Drifted"})
				m := *ref.Approved
				m.Routing.Default = true
				if err := WriteManifest(filepath.Join(home, "drifted"), &m, true); err != nil {
					t.Fatal(err)
				}
				return ref
			}(),
			{Path: "~/missing", Approved: &BrainManifest{Version: 1, ID: "missing", Name: "Missing", PromptFile: defaultPromptFile}},
		}
	}
	tests := []struct {
		name, config string
		args         []string
	}{
		{"summary", "", nil},
		{"route", "", []string{
			"--route", harnessDM, "--route", "15559876543@s.whatsapp.net",
			"--route", harnessGroup, "--route", "120363000000000001@g.us",
		}},
		{"anyone-may-dm", `"allow_any_dm": true, "allowed_numbers": []`, nil},
		{"does-not-load", `"queue": {"mode": "sometimes"}`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root, cfg := cliFixture(t, tc.config, brains)
			out, code := runBridgeCLI(t, root, append([]string{"check", "--config", cfg}, tc.args...)...)
			s := newScrubber(root)
			assertGolden(t, "cli/check-"+tc.name+".golden", []byte(s.text(out)+"exit "+strconv.Itoa(code)+"\n"))
		})
	}
}

// TestGoldenSandboxProfiles pins the sandbox profile, as `fridolin check
// --print-sandbox` prints it, for each part of the profile that settings
// change: the brain's own folder and the others', the data dir, media and
// database elsewhere, the calendar files in a credentials dir, deny_paths
// and allow_paths, and git's config readable or denied. The folders above
// the test's root differ between systems; they are <ABOVE>, once a block.
func TestGoldenSandboxProfiles(t *testing.T) {
	gitconfig := func(home string) {
		if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = You\n[include]\n\tpath = ~/.gitconfig-work\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	one := func(home string) []BrainRef {
		gitconfig(home)
		return []BrainRef{brainFolder(t, "~/main", BrainManifest{ID: "main", Routing: BrainRouting{HandlesDMs: true}})}
	}
	several := func(home string) []BrainRef {
		gitconfig(home)
		return []BrainRef{
			brainFolder(t, "~/brains/main", BrainManifest{ID: "main", Routing: BrainRouting{HandlesDMs: true}}),
			brainFolder(t, "~/brains/family", BrainManifest{ID: "family", Routing: BrainRouting{Groups: []string{harnessGroup}}}),
			// Outside HOME.
			brainFolder(t, filepath.Join(filepath.Dir(home), "elsewhere", "work"), BrainManifest{ID: "work"}),
		}
	}
	tests := []struct {
		name, config string
		brains       func(home string) []BrainRef
	}{
		{"own-brain", "", one},
		{"other-brains", "", several},
		{"data-elsewhere", `"data_dir": "<ROOT>/data", "db_path": "<ROOT>/db/bridge.db", "media_dir": "<ROOT>/inbox"`, one},
		{"credentials-dir", `"sandbox": {"credentials_dir": "~/creds"}`, several},
		// One deny inside the brain's own folder, which applies again after
		// the folder's allow.
		{"deny-allow-paths", `"sandbox": {"deny_paths": ["~/clients", "<ROOT>/private", "~/main/private"], "allow_paths": ["~/Documents/shared", "~/.ssh/known_hosts"]}`, one},
		{"git-deny", `"sandbox": {"git_config": "deny"}`, one},
		{"inherit-user-config", `"inherit_claude_user_config": true`, several},
		{"plaud-brain-access", `"plaud": {"brain_access": true}`, one},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// <ROOT> in the settings is the test's folder, known only now.
			root, cfg := cliFixture(t, "", tc.brains)
			if tc.config != "" {
				data, err := os.ReadFile(cfg)
				if err != nil {
					t.Fatal(err)
				}
				data = bytes.TrimSuffix(data, []byte("}"))
				data = append(data, ", "+strings.ReplaceAll(tc.config, "<ROOT>", root)+"}"...)
				if err := os.WriteFile(cfg, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			out, code := runBridgeCLI(t, root, "check", "--config", cfg, "--print-sandbox", "main")
			if code != 0 {
				t.Fatalf("check --print-sandbox: exit %d\n%s", code, out)
			}
			// Only the profile, after the summary and a blank line.
			_, profile, ok := strings.Cut(out, "\n\n")
			if !ok {
				t.Fatalf("no profile in:\n%s", out)
			}
			assertGolden(t, "sandbox/"+tc.name+".sb", []byte(newScrubber(root).lines(profile)))
		})
	}
}

// TestGoldenStarterConfig pins the config `fridolin init` writes. The
// sandbox is on where it can run, on macOS.
func TestGoldenStarterConfig(t *testing.T) {
	root, _ := fixtureHome(t)
	cfg := filepath.Join(root, "home", ".fridolin", "config.json")
	out, code := runBridgeCLI(t, root, "init", "--config", cfg, "--claude-path", "/opt/claude/bin/claude")
	if code != 0 {
		t.Fatalf("init: exit %d\n%s", code, out)
	}
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Sandbox struct {
			Enabled bool `json:"enabled"`
		} `json:"sandbox"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if file.Sandbox.Enabled != (runtime.GOOS == "darwin") {
		t.Errorf("sandbox.enabled = %v on %s", file.Sandbox.Enabled, runtime.GOOS)
	}
	s := newScrubber(root).maskKeys("sandbox.enabled")
	assertGolden(t, "cli/init-config.golden", s.json(t, data))
	assertGolden(t, "cli/init-log.golden", []byte(s.text(out)))

	// A second init leaves the file alone.
	out, code = runBridgeCLI(t, root, "init", "--config", cfg)
	assertGolden(t, "cli/init-again.golden", []byte(newScrubber(root).text(out)+"exit "+strconv.Itoa(code)+"\n"))
}
