package main

import (
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout returns what fn prints to standard output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	stdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = stdout }()
	out := make(chan string)
	go func() {
		data, _ := io.ReadAll(r)
		out <- string(data)
	}()
	fn()
	w.Close()
	return <-out
}

func TestCheckShowsWhoMayDM(t *testing.T) {
	const (
		owner    = "15551234567@s.whatsapp.net"
		stranger = "15559876543@s.whatsapp.net"
		ignored  = "(not in allowed_numbers: ignored)"
	)
	tests := []struct {
		name   string
		access string // config fields that decide DM access
		allow  string // the DM allow line
		// whether each route line says the chat is ignored
		ownerIgnored, strangerIgnored bool
	}{
		{"unset flag and empty list", `"allowed_numbers": []`, "DM allow:  nobody", true, true},
		{"explicit true and empty list", `"allowed_numbers": [], "allow_any_dm": true`, "DM allow:  anyone", false, false},
		{"numbers listed", `"allowed_numbers": ["15551234567"], "allow_any_dm": true`, "DM allow:  [15551234567]", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			path := writeConfig(t, `{
				"brain_mode": true,
				"data_dir": "`+home+`/data"`+brainDirsJSON(t,
				brainFolder(t, home+"/main", BrainManifest{ID: "main", Routing: BrainRouting{HandlesDMs: true}}),
			)+`,
				`+tc.access+`
			}`)
			out := captureStdout(t, func() {
				checkCmd([]string{"--config", path, "--route", owner, "--route", stranger})
			})
			if !strings.Contains(out, tc.allow) {
				t.Errorf("want %q in:\n%s", tc.allow, out)
			}
			for jid, wantIgnored := range map[string]bool{owner: tc.ownerIgnored, stranger: tc.strangerIgnored} {
				line := lineWith(out, "route "+jid)
				if line == "" {
					t.Fatalf("no route line for %s in:\n%s", jid, out)
				}
				if got := strings.Contains(line, ignored); got != wantIgnored {
					t.Errorf("%q: ignored = %v, want %v", line, got, wantIgnored)
				}
			}
		})
	}
}

// lineWith returns the first line of out that contains s.
func lineWith(out, s string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, s) {
			return l
		}
	}
	return ""
}

func TestConfigPath(t *testing.T) {
	// The config is the --config value, else FRIDOLIN_CONFIG, else the data
	// dir's. A config.json in the working directory used to win over the
	// data dir's, so a command run from any folder holding one took that
	// file, and a dashboard save rewrote it. CONFIG_FILE was the variable's
	// old name: honouring it silently would be the same surprise, ignoring
	// it silently another one.
	tests := []struct {
		name                 string
		flag                 string // --config; "" means not given
		fridolinCfg, cfgFile string // FRIDOLIN_CONFIG and CONFIG_FILE; "" means unset
		cwdConfig            bool   // a config.json lies in the working directory
		want                 string // "home/..." or "other/...", relative to the temp dirs
		warnsOldName         bool
	}{
		{"the data dir's by default", "", "", "", false, "home/.fridolin/config.json", false},
		{"not one in the working directory", "", "", "", true, "home/.fridolin/config.json", false},
		{"FRIDOLIN_CONFIG", "", "other/env.json", "", true, "other/env.json", false},
		{"--config wins over FRIDOLIN_CONFIG", "other/flag.json", "other/env.json", "", false, "other/flag.json", false},
		{"CONFIG_FILE is ignored, and said so", "", "", "other/old.json", false, "home/.fridolin/config.json", true},
		{"CONFIG_FILE next to FRIDOLIN_CONFIG", "", "other/env.json", "other/old.json", false, "other/env.json", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home, other := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			abs := func(p string) string {
				if p == "" {
					return ""
				}
				if rest, ok := strings.CutPrefix(p, "home/"); ok {
					return filepath.Join(home, rest)
				}
				return filepath.Join(other, strings.TrimPrefix(p, "other/"))
			}
			clearEnv(t, "FRIDOLIN_CONFIG", "CONFIG_FILE")
			if tc.fridolinCfg != "" {
				t.Setenv("FRIDOLIN_CONFIG", abs(tc.fridolinCfg))
			}
			if tc.cfgFile != "" {
				t.Setenv("CONFIG_FILE", abs(tc.cfgFile))
			}
			wd := t.TempDir()
			if tc.cwdConfig {
				if err := os.WriteFile(filepath.Join(wd, "config.json"), []byte(`{"_comment": "some other project's"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(wd)
			var args []string
			if tc.flag != "" {
				args = []string{"--config", abs(tc.flag)}
			}
			logs := captureLog(t)
			out := captureStdout(t, func() { checkCmd(args) })
			if want := "Config OK: " + abs(tc.want) + "\n"; !strings.HasPrefix(out, want) {
				t.Errorf("check printed %q, want it to start with %q", out, want)
			}
			warned := logContains(logs, "CONFIG_FILE="+abs(tc.cfgFile)+" is ignored: set FRIDOLIN_CONFIG")
			if warned != tc.warnsOldName {
				t.Errorf("warns about CONFIG_FILE: %v, want %v (log %q)", warned, tc.warnsOldName, logs())
			}
		})
	}
}

func TestRunLogsConfigWarningsToTheDashboard(t *testing.T) {
	// run picks the config only once its log ring is in place. Picked
	// before, the CONFIG_FILE warning would reach stderr but not the
	// dashboard's log.
	t.Setenv("HOME", t.TempDir())
	clearEnv(t, "FRIDOLIN_CONFIG")
	old := filepath.Join(t.TempDir(), "old.json")
	t.Setenv("CONFIG_FILE", old)
	prev := log.Writer()
	t.Cleanup(func() { log.SetOutput(prev) })
	var console strings.Builder
	ring, _ := runLog(&console, "")
	want := "CONFIG_FILE=" + old + " is ignored"
	lines, _ := ring.Since(0, 50)
	var inRing bool
	for _, l := range lines {
		inRing = inRing || strings.Contains(l.Text, want)
	}
	if !inRing {
		t.Errorf("the dashboard's log does not say %q: %+v", want, lines)
	}
	if !strings.Contains(console.String(), want) {
		t.Errorf("the console does not say %q: %q", want, console.String())
	}
}

func TestSetupStopsOnConfigFile(t *testing.T) {
	// CONFIG_FILE was the old name of FRIDOLIN_CONFIG, but other software
	// exports it too, so setup.sh stops before it writes anything and names
	// both ways on: pass the value on as FRIDOLIN_CONFIG if it is
	// Fridolin's config, or leave the variable out if it is not. Naming only
	// the first would install the service with the other software's config.
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	// PATH holds what setup.sh needs before it looks for go, and no go: one
	// that does not stop at CONFIG_FILE stops at "Go is not installed"
	// rather than build the bridge and install a service.
	bin := t.TempDir()
	for _, tool := range []string{"dirname", "sed", "uname"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(repoDir, "setup.sh")
	other := filepath.Join(t.TempDir(), "other-tool.json")
	tests := []struct {
		name       string
		configFile string // "" means unset
		want       []string
	}{
		{"CONFIG_FILE set", other, []string{
			"CONFIG_FILE is no longer read",
			`FRIDOLIN_CONFIG="` + other + `" ` + script + " --no-service",
			"env -u CONFIG_FILE " + script + " --no-service",
		}},
		{"CONFIG_FILE unset", "", []string{"Go is not installed"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cmd := exec.Command(bash, script, "--no-service")
			cmd.Env = []string{"HOME=" + home, "PATH=" + bin}
			if tc.configFile != "" {
				cmd.Env = append(cmd.Env, "CONFIG_FILE="+tc.configFile)
			}
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("setup.sh: %v, want exit status 1:\n%s", err, out)
			}
			for _, w := range tc.want {
				if !strings.Contains(string(out), w) {
					t.Errorf("setup.sh does not say %q:\n%s", w, out)
				}
			}
			if tc.configFile == "" && strings.Contains(string(out), "CONFIG_FILE") {
				t.Errorf("setup.sh mentions CONFIG_FILE, which is not set:\n%s", out)
			}
			if entries, _ := os.ReadDir(home); len(entries) > 0 {
				t.Errorf("setup.sh wrote to HOME before it stopped: %v", entries)
			}
		})
	}
}

func TestTestsDoNotRunInTheCheckout(t *testing.T) {
	// A relative path would otherwise find a config.json a developer keeps
	// in the checkout.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("go.mod"); err == nil {
		t.Errorf("the tests run in the checkout (%s)", wd)
	}
	if tmp := canonicalDir(os.TempDir()); !isWithin(canonicalDir(wd), tmp) {
		t.Errorf("working directory %s is not inside the temp dir %s", wd, tmp)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "go.mod")); err != nil {
		t.Errorf("repoDir %q is not the checkout: %v", repoDir, err)
	}
}

func TestTestsStartWithATempHome(t *testing.T) {
	// A test that forgets t.Setenv("HOME", ...) must find ~ and the default
	// data dir in a temp dir, not in the home of whoever runs the tests.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if tmp := canonicalDir(os.TempDir()); !isWithin(canonicalDir(home), tmp) {
		t.Errorf("home %s is not inside the temp dir %s", home, tmp)
	}
	if home != testHome {
		t.Errorf("home %s is not the one TestMain made (%q)", home, testHome)
	}
}

func TestShellBridgeVariablesDoNotReachTests(t *testing.T) {
	// Runs the test binary again with every variable the bridge reads set,
	// the way a developer's shell might export them for a real bridge, and
	// checks from inside that TestMain cleared them before any test ran.
	if os.Getenv("WCB_CHECK_BRIDGE_VARIABLES_CLEARED") == "1" {
		for _, k := range bridgeVariables() {
			if _, ok := os.LookupEnv(k); ok {
				t.Errorf("%s from the shell reached the tests", k)
			}
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestShellBridgeVariablesDoNotReachTests$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), "WCB_CHECK_BRIDGE_VARIABLES_CLEARED=1")
	notTheTests := filepath.Join(t.TempDir(), "not-the-tests")
	for _, k := range bridgeVariables() {
		cmd.Env = append(cmd.Env, k+"="+notTheTests)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--- PASS: TestShellBridgeVariablesDoNotReachTests") {
		t.Fatalf("the check did not run:\n%s", out)
	}
}
