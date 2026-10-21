package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
)

func TestCheckHostMount(t *testing.T) {
	home, data := "/Users/you", "/Users/you/.fridolin"
	for p, want := range map[string]string{
		"/Users/you/Personal":        "",
		"/Users/you/.fridolin-other": "",
		"/Users/you":                 "home folder",
		"/Users/you/.fridolin":       "native data dir",
		"/Users/you/.fridolin/media": "native data dir",
		"/Users/you/.ssh":            "~/.ssh",
		"/Users/you/Library":         "~/Library/Keychains",
		"/opt/data":                  "outside the home folder",
	} {
		err := checkHostMount(p, home, data)
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", p, err, want)
		}
	}
}

// migrateHome is a native install in a temp home folder: a data dir, a
// brain folder outside it, a credentials folder and the Plaud login.
func migrateHome(t *testing.T) (home, cfgPath string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	makeFiles(t, home, "Personal/brain.json", "Personal/bin/", "private/tools/x.py", ".secrets/google_calendar_token_personal.json",
		".plaud/tokens.json", ".gitconfig", "data/dashboard.token", "data/media/personal/a.jpg", "data/migration/old.db",
		"data/brains/personal/codex/auth.json")
	cfgPath = filepath.Join(home, "data", "config.json")
	cfg := `{"data_dir": "~/data", "claude_path": "~/private/claude-wrapper.sh", "agent_provider": "codex",
  "brain_dirs": [{"path": "~/Personal", "approved": {"version": 1, "id": "personal", "name": "Personal"}}],
  "sandbox": {"credentials_dir": "~/.secrets"},
  "startup_checks": {"required_files": ["~/.secrets/google_calendar_token_personal.json"]},
  "plaud": {"enabled": true, "brain": "personal", "brain_access": true}}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, cfgPath
}

func TestNativeMountsAndOverride(t *testing.T) {
	home, cfgPath := migrateHome(t)
	cfg, err := config.LoadExisting(cfgPath, config.Options{Paths: config.Paths{Home: home}, GOOS: "darwin", Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	mounts, notes, err := nativeMounts(cfg, mountInputs{home: home, extra: []string{"~/private"}, kind: pathKindOf})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range mounts {
		got = append(got, strings.TrimPrefix(m.Path, home)+map[bool]string{true: ":ro", false: ":rw"}[m.ReadOnly])
	}
	want := []string{"/Personal:rw", "/.secrets:rw", "/.plaud:rw", "/.gitconfig:ro", "/private:ro"}
	if !slices.Equal(got, want) {
		t.Errorf("mounts %q, want %q (notes %q)", got, want, notes)
	}
	unit := installedUnit{Env: map[string]string{"FRIDOLIN_BRAIN_PATH": home + "/Personal/bin:/elsewhere/bin"}}
	bp := brainPathFromUnit(unit, mounts)
	if bp != home+"/Personal/bin" {
		t.Errorf("brain path %q", bp)
	}
	override := strings.ReplaceAll(string(composeOverride(home, "<CONFIG>", mounts, []string{"FRIDOLIN_BRAIN_PATH=" + bp})), home, "<HOME>")
	assertGolden(t, "cli/core-host-override.golden", []byte(override))

	if _, _, err := nativeMounts(cfg, mountInputs{home: home, extra: []string{"~/data/media"}, kind: pathKindOf}); err == nil {
		t.Error("a mount inside the data dir was taken")
	}
	if _, _, err := nativeMounts(cfg, mountInputs{home: home, extra: []string{"~/missing"}, kind: pathKindOf}); err == nil {
		t.Error("a missing --mount was taken")
	}
}

func TestEnsureClaudeToken(t *testing.T) {
	unit := installedUnit{Env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "test-oauth-value"}}
	secrets := mapSecrets{}
	line, err := ensureClaudeToken(secrets, unit, true)
	if err != nil || secrets[hostsecrets.ClaudeOAuthToken] != "" || strings.Contains(line, "test-oauth") {
		t.Fatalf("dry run: %q %v %v", line, err, secrets)
	}
	line, err = ensureClaudeToken(secrets, unit, false)
	if err != nil || secrets[hostsecrets.ClaudeOAuthToken] != "test-oauth-value" || strings.Contains(line, "test-oauth") {
		t.Fatalf("copy: %q %v", line, err)
	}
	secrets[hostsecrets.ClaudeOAuthToken] = "kept"
	if _, err := ensureClaudeToken(secrets, unit, false); err != nil || secrets[hostsecrets.ClaudeOAuthToken] != "kept" {
		t.Error("an existing token was replaced")
	}
	if line, _ := ensureClaudeToken(mapSecrets{}, installedUnit{}, false); !strings.Contains(line, "setup-token") {
		t.Errorf("no token anywhere: %q", line)
	}
}

func TestCoreStartAddsTheHostOverride(t *testing.T) {
	d, ran := hostFixture(t, mapSecrets{hostsecrets.AgeIdentity: "AGE-SECRET-KEY-1OLD"})
	override := filepath.Join(filepath.Dir(d.composePath), hostOverrideName)
	if err := os.MkdirAll(filepath.Dir(override), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := coreHost(context.Background(), "start", nil, coreEnv{stdout: io.Discard, stderr: io.Discard}, d); code != 0 {
		t.Fatal(code)
	}
	if want := []string{"compose", "-f", d.composePath, "-f", override, "up", "-d"}; !slices.Equal((*ran)[0].args, want) {
		t.Errorf("args %q", (*ran)[0].args)
	}
	*ran = nil
	if code := coreHost(context.Background(), "stop", []string{"--compose", "/x/compose.yaml"}, coreEnv{stdout: io.Discard, stderr: io.Discard}, d); code != 0 {
		t.Fatal(code)
	}
	if slices.Contains((*ran)[0].args, override) {
		t.Error("the override went with a compose file of one's own")
	}
}

// migrateDeps is a host whose docker runs the fridolin commands a one-off
// container would, in this process, on volume (the container's /data).
func migrateDeps(t *testing.T, home, volume string, secrets mapSecrets) (hostDeps, *[]ranCmd) {
	d, ran := hostFixture(t, secrets)
	d.home, d.goos, d.uid, d.cacheDir = home, "darwin", 501, t.TempDir()
	d.unitPath = filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
	d.now = func() time.Time { return time.Date(2026, 10, 21, 1, 0, 0, 0, time.UTC) }
	d.sleep = func(time.Duration) {}
	d.newIdentity = defaultHostDeps(coreEnv{}).newIdentity
	d.run = func(_ context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
		*ran = append(*ran, ranCmd{name, args, env})
		i := slices.Index(args, "core")
		if args[0] != "compose" || !slices.Contains(args, "run") || i < 0 || i+1 >= len(args) || args[i+1] != "core" {
			return nil
		}
		cmd := slices.Clone(args[i+2:])
		local := ""
		for j, a := range args {
			if a == "-v" && strings.Contains(args[j+1], ":"+migrateMountPoint) {
				local, _, _ = strings.Cut(args[j+1], ":")
			}
		}
		for j, a := range cmd {
			if a == migrateMountPoint {
				cmd[j] = local
			}
		}
		if cmd[0] == "export" {
			cmd = append(cmd, "--config", filepath.Join(volume, "config.json"))
		}
		environ := append(slices.Clone(env), "FRIDOLIN_HOME="+volume)
		if code := runCore(cmd, coreEnv{environ: environ, stdout: stdout, stderr: stderr}); code != 0 {
			return errors.New("exit status " + string(rune('0'+code)))
		}
		return nil
	}
	return d, ran
}

func TestCoreMigrateFromAndToNative(t *testing.T) {
	home, cfgPath := migrateHome(t)
	plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>EnvironmentVariables</key><dict>` +
		`<key>CLAUDE_CODE_OAUTH_TOKEN</key><string>test-oauth-from-plist</string>` +
		`<key>FRIDOLIN_BRAIN_PATH</key><string>` + home + `/Personal/bin</string></dict></dict></plist>`
	makeFiles(t, home, "Library/LaunchAgents/")
	if err := os.WriteFile(filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	volume := filepath.Join(t.TempDir(), "volume")
	secrets := mapSecrets{}
	d, ran := migrateDeps(t, home, volume, secrets)
	var stdout, stderr bytes.Buffer
	env := coreEnv{environ: []string{"HOME=" + home}, stdout: &stdout, stderr: &stderr}

	// A dry run changes nothing.
	if code := coreMigrate(context.Background(), []string{"--from-native", "--dry-run", "--local", "--config", cfgPath, "--mount", "~/private"}, env, d); code != 0 {
		t.Fatalf("dry run: %d %s", code, stderr.String())
	}
	if len(secrets) != 0 || len(*ran) != 0 || pathKindOf(filepath.Join(filepath.Dir(d.composePath), hostOverrideName)) != "" {
		t.Fatalf("a dry run did something: %v %v", secrets, *ran)
	}
	if !strings.Contains(stdout.String(), "launchctl disable gui/501/"+serviceLabel) || strings.Contains(stdout.String(), "test-oauth") {
		t.Errorf("plan: %s", stdout.String())
	}

	stdout.Reset()
	if code := coreMigrate(context.Background(), []string{"--from-native", "--local", "--config", cfgPath, "--mount", "~/private"}, env, d); code != 0 {
		t.Fatalf("migrate: %d\n%s\n%s", code, stdout.String(), stderr.String())
	}
	if secrets[hostsecrets.ClaudeOAuthToken] != "test-oauth-from-plist" || secrets[hostsecrets.AgeIdentity] == "" {
		t.Errorf("secrets not stored: %v", secrets)
	}
	if strings.Contains(stdout.String()+stderr.String(), "test-oauth") {
		t.Error("the token was printed")
	}
	override, err := os.ReadFile(filepath.Join(filepath.Dir(d.composePath), hostOverrideName))
	if err != nil || !strings.Contains(string(override), home+"/Personal") || !strings.Contains(string(override), "FRIDOLIN_BRAIN_PATH="+home+"/Personal/bin") {
		t.Errorf("override: %v\n%s", err, override)
	}
	for rel, want := range map[string]bool{"dashboard.token": true, "media/personal/a.jpg": true, "brains/personal/codex/auth.json": true, "migration": false} {
		if (pathKindOf(filepath.Join(volume, rel)) != "") != want {
			t.Errorf("%s in the volume: want %v", rel, want)
		}
	}
	c, err := os.ReadFile(filepath.Join(volume, "config.json"))
	if err != nil || strings.Contains(string(c), "claude-wrapper") || !strings.Contains(string(c), `/private"`) || !strings.Contains(string(c), `"data_dir": "`+volume) {
		t.Errorf("the volume's config: %v\n%s", err, c)
	}
	if _, err := os.Stat(filepath.Join(home, "data", "core")); err == nil {
		t.Error("the export wrote into the native data dir")
	}

	// And back: the native data dir's files go to migration/<time>/replaced.
	if err := os.WriteFile(filepath.Join(volume, "dashboard.token"), []byte("rotated in the core"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := coreMigrate(context.Background(), []string{"--to-native", "--local", "--config", cfgPath}, env, d); code != 0 {
		t.Fatalf("to-native: %d\n%s\n%s", code, stdout.String(), stderr.String())
	}
	native := filepath.Join(home, "data")
	if b, _ := os.ReadFile(filepath.Join(native, "dashboard.token")); string(b) != "rotated in the core" {
		t.Errorf("dashboard.token %q", b)
	}
	aside := filepath.Join(native, "migration", "20261021T010000Z", "replaced")
	if pathKindOf(filepath.Join(aside, "dashboard.token")) != "file" || pathKindOf(filepath.Join(native, "migration", "old.db")) != "file" {
		t.Error("the native data dir's files were not kept")
	}
	c, _ = os.ReadFile(filepath.Join(native, "config.json"))
	if !strings.Contains(string(c), `"data_dir": "~/data"`) || !strings.Contains(string(c), "claude-wrapper") || strings.Contains(string(c), `/private"`) {
		t.Errorf("the native config:\n%s", c)
	}
}

func TestCoreMigrateFlags(t *testing.T) {
	d, _ := hostFixture(t, mapSecrets{})
	for _, args := range [][]string{{}, {"--from-native", "--to-native"}, {"--to-native", "--rehearse"}, {"--from-native", "x"}} {
		if code := coreMigrate(context.Background(), args, coreEnv{stdout: io.Discard, stderr: io.Discard}, d); code != 2 {
			t.Errorf("%q: exit %d", args, code)
		}
	}
}
