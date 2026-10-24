package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/fsx"
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
// with HOME at <root>/home, its temp folders (TMPDIR, tmux's TMUX_TMPDIR)
// in <root>/tmp, and nothing else of the test's environment but PATH's
// system folders and the fake Claude CLI's switch. It returns what the
// command printed, stdout then stderr, and its exit code.
func runBridgeCLI(t *testing.T, root string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(linkBridgeBinary(t, root), args...)
	cmd.Dir = root
	// The sandbox profile closes the unix sockets in the temp folders; in
	// /tmp, which is /private/tmp on macOS only, it would name them
	// differently on each system.
	tmp := filepath.Join(fsx.Canonical(root), "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd.Env = []string{
		"FRIDOLIN_TEST_RUN_MAIN=1",
		"FRIDOLIN_FAKE_CLAUDE=1",
		"HOME=" + filepath.Join(root, "home"),
		"PATH=/usr/bin:/bin",
		"TMPDIR=" + tmp,
		"TMUX_TMPDIR=" + tmp,
		"GORACE=atexit_sleep_ms=0",
		// A fixed zone, for fridolin init and doctor to say the same everywhere.
		"TZ=Europe/Zurich",
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
func cliFixture(t *testing.T, configValue string, brains func(home string) []config.BrainRef) (root, cfgPath string) {
	t.Helper()
	root, home := fixtureHome(t)
	var refs []config.BrainRef
	if brains != nil {
		refs = brains(home)
	}
	cfgPath = filepath.Join(root, "config.json")
	body := `{
		"claude_path": "` + filepath.Join(root, "bin", "fridolin") + `",
		"data_dir": "~/.fridolin",
		"allowed_numbers": ["15551234567"],
		"allowed_groups": ["120363000000000000@g.us"]` + brainDirsJSON(t, refs...)
	if configValue != "" {
		body += ", " + configValue
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
	// Serial: runs fridolin's commands in the process, which read its HOME
	// and redirect its standard logger.
	brains := func(home string) []config.BrainRef {
		return []config.BrainRef{
			brainFolder(t, "~/main", manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}}),
			brainFolder(t, "~/family", manifest.Manifest{ID: "family", Name: "Family", Routing: manifest.Routing{Groups: []string{harnessGroup}}}),
			// A folder whose brain.json was changed after it was approved.
			func() config.BrainRef {
				ref := brainFolder(t, "~/drifted", manifest.Manifest{ID: "drifted", Name: "Drifted"})
				m := *ref.Approved
				m.Routing.Default = true
				if err := manifest.Write(filepath.Join(home, "drifted"), &m, true); err != nil {
					t.Fatal(err)
				}
				return ref
			}(),
			{Path: "~/missing", Approved: &manifest.Manifest{Version: 1, ID: "missing", Name: "Missing", PromptFile: manifest.DefaultPromptFile}},
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
// and allow_paths, and git's config readable or denied. The folders between
// / and the test's root differ between systems; they are <ABOVE>, once a
// block. So do the bridge's programs outside the test's folder, which it
// leaves out (withoutSystemPrograms).
func TestGoldenSandboxProfiles(t *testing.T) {
	// Serial: runs fridolin's commands in the process, which read its HOME
	// and redirect its standard logger.
	gitconfig := func(home string) {
		if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = You\n[include]\n\tpath = ~/.gitconfig-work\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	one := func(home string) []config.BrainRef {
		gitconfig(home)
		return []config.BrainRef{brainFolder(t, "~/main", manifest.Manifest{ID: "main", Routing: manifest.Routing{HandlesDMs: true}})}
	}
	several := func(home string) []config.BrainRef {
		gitconfig(home)
		return []config.BrainRef{
			brainFolder(t, "~/brains/main", manifest.Manifest{ID: "main", Routing: manifest.Routing{HandlesDMs: true}}),
			brainFolder(t, "~/brains/family", manifest.Manifest{ID: "family", Routing: manifest.Routing{Groups: []string{harnessGroup}}}),
			// Outside HOME.
			brainFolder(t, filepath.Join(filepath.Dir(home), "elsewhere", "work"), manifest.Manifest{ID: "work"}),
		}
	}
	tests := []struct {
		name, config string
		brains       func(home string) []config.BrainRef
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
			// tmux names its folder after the user's id.
			profile = strings.ReplaceAll(profile, "/tmux-"+strconv.Itoa(os.Getuid()), "/tmux-<UID>")
			assertGolden(t, "sandbox/"+tc.name+".sb", []byte(withoutSystemPrograms(newScrubber(root).lines(profile))))
		})
	}
}

// systemWriteDenyRE matches a write deny of a path the masks leave as it
// is, one outside the test's folder.
var systemWriteDenyRE = regexp.MustCompile(`^\(deny file-write\* \((literal|subpath) "/[^"]*"\)\)$`)

// withoutSystemPrograms drops from a masked profile the write denies of
// section 4 that name a path outside the test's folder: the programs the
// bridge runs and the folders it finds them in. Which there are depends on
// what the system has installed (node, rclone, Homebrew) and how its
// folders link (/bin is /usr/bin on Linux), so they differ between
// systems; TestSandboxProfileProtectsTheBridgesPrograms pins them. Those
// in the test's folder stay.
func withoutSystemPrograms(profile string) string {
	var out []string
	section4 := false
	for _, l := range strings.Split(profile, "\n") {
		switch {
		case strings.HasPrefix(l, ";; "):
			section4 = strings.HasPrefix(l, ";; 4. ")
		case l == "":
			section4 = false
		case section4 && systemWriteDenyRE.MatchString(l):
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func TestWithoutSystemPrograms(t *testing.T) {
	t.Parallel()
	lines := func(l ...string) string { return strings.Join(l, "\n") }
	tests := []struct{ name, in, want string }{
		{"a program and a PATH folder outside the test's folder", lines(
			";; 4. Kept from every brain, whatever is allowed above",
			`(deny file-write* (literal "<ROOT>/bin/fridolin"))`,
			`(deny file-write* (literal "/opt/homebrew/bin/rclone"))`,
			`(deny file-write* (subpath "/usr/bin"))`,
			`(deny file-write* (subpath "<HOME>/.local/bin"))`,
		), lines(
			";; 4. Kept from every brain, whatever is allowed above",
			`(deny file-write* (literal "<ROOT>/bin/fridolin"))`,
			`(deny file-write* (subpath "<HOME>/.local/bin"))`,
		)},
		{"other rules of section 4 stay", lines(
			";; 4. Kept from every brain, whatever is allowed above",
			`(deny file-read* (literal "/CLAUDE.md"))`,
			`(deny file-write* (literal "<ABOVE>"))  [×each folder above]`,
		), lines(
			";; 4. Kept from every brain, whatever is allowed above",
			`(deny file-read* (literal "/CLAUDE.md"))`,
			`(deny file-write* (literal "<ABOVE>"))  [×each folder above]`,
		)},
		{"other sections stay", lines(
			";; 1. Writing is denied everywhere; this brain's own places are allowed below",
			`(deny file-write* (subpath "/"))`,
			"",
			";; 4. Kept from every brain, whatever is allowed above",
			`(deny file-write* (subpath "/bin"))`,
			"",
			";; 7. Starting code outside the sandbox",
			`(deny file-write* (subpath "/usr/bin"))`,
		), lines(
			";; 1. Writing is denied everywhere; this brain's own places are allowed below",
			`(deny file-write* (subpath "/"))`,
			"",
			";; 4. Kept from every brain, whatever is allowed above",
			"",
			";; 7. Starting code outside the sandbox",
			`(deny file-write* (subpath "/usr/bin"))`,
		)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := withoutSystemPrograms(tc.in); got != tc.want {
				t.Errorf("got\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

// TestGoldenStarterConfig pins the config `fridolin init` writes. The
// sandbox is on where it can run, on macOS.
func TestGoldenStarterConfig(t *testing.T) {
	// Serial: runs fridolin's commands in the process, which read its HOME
	// and redirect its standard logger.
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
