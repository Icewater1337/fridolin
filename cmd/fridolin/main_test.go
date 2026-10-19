package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/testutil"
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
			// check runs the CLI for its version: the fake, not one on PATH.
			path := writeConfig(t, `{
				"brain_mode": true,
				"claude_path": "`+fakeClaudeEnv(t, 0)+`",
				"data_dir": "`+home+`/data"`+brainDirsJSON(t,
				brainFolder(t, home+"/main", manifest.Manifest{ID: "main", Routing: manifest.Routing{HandlesDMs: true}}),
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

// check says for each allowed group whose messages there reach its brain,
// as groupSenderAllowed decides: the numbers its group_members entry
// lists, every member for ["*"], and without an entry whom allowed_numbers
// admits.
func TestCheckShowsWhoIsHeardInGroups(t *testing.T) {
	const group = "120363000000000000@g.us"
	cases := []struct {
		name   string
		access string // config fields that decide who is heard
		heard  string // the group's line
	}{
		{"listed", `"group_members": {"` + group + `": ["+1 555 987 6543", "15551234567"]}`, "heard in " + group + ": [15559876543 15551234567] (group_members)"},
		{"everyone", `"group_members": {"` + group + `": ["*"]}`, "heard in " + group + `: every member (group_members is ["*"])`},
		{"an entry of no one", `"group_members": {"` + group + `": []}`, "heard in " + group + ": nobody (its group_members entry lists no one)"},
		{"no entry", "", "heard in " + group + ": [15551234567] (no group_members entry: allowed_numbers)"},
		{"no entry, nobody may DM", `"allowed_numbers": []`, "heard in " + group + ": nobody (no group_members entry, and allowed_numbers is empty)"},
		{"no entry, anyone may DM", `"allowed_numbers": [], "allow_any_dm": true`, "heard in " + group + ": every member (no group_members entry, allow_any_dm is true and allowed_numbers is empty)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			access := `"allowed_numbers": ["15551234567"]`
			if strings.Contains(c.access, "allowed_numbers") {
				access = c.access
			} else if c.access != "" {
				access += ", " + c.access
			}
			path := writeConfig(t, `{
				"claude_path": "`+fakeClaudeEnv(t, 0)+`",
				"data_dir": "`+home+`/data",
				"allowed_groups": ["`+group+`"],
				`+access+`
			}`)
			var out bytes.Buffer
			if err := writeCheck(&out, checkOptions{config: path}); err != nil {
				t.Fatal(err)
			}
			if got := lineWith(out.String(), "heard in"); strings.TrimSpace(got) != c.heard {
				t.Errorf("got %q, want %q in:\n%s", got, c.heard, out.String())
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
			testutil.ClearEnv(t, "FRIDOLIN_CONFIG", "CONFIG_FILE")
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
			// Every config the case names is there: check refuses one that
			// is not, and must still say which it took.
			for _, p := range []string{"home/.fridolin/config.json", tc.flag, tc.fridolinCfg, tc.cfgFile} {
				if p == "" {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(abs(p)), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(abs(p), []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
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
	testutil.ClearEnv(t, "FRIDOLIN_CONFIG")
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

func TestRunLogNamesTheConfigFile(t *testing.T) {
	// A log excerpt must say which config the bridge ran on, and why that
	// one: a service passes --config, a shell may export FRIDOLIN_CONFIG.
	home, other := fsx.Canonical(t.TempDir()), fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	tests := []struct {
		name, flag, env string // "" means none
		path, source    string
	}{
		{"default", "", "", home + "/.fridolin/config.json", "default"},
		{"FRIDOLIN_CONFIG", "", other + "/env.json", other + "/env.json", "FRIDOLIN_CONFIG"},
		{"--config", other + "/flag.json", other + "/env.json", other + "/flag.json", "--config"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testutil.ClearEnv(t, "FRIDOLIN_CONFIG", "CONFIG_FILE")
			if tc.env != "" {
				t.Setenv("FRIDOLIN_CONFIG", tc.env)
			}
			prev := log.Writer()
			t.Cleanup(func() { log.SetOutput(prev) })
			var console strings.Builder
			ring, path := runLog(&console, tc.flag)
			if path != tc.path {
				t.Errorf("runLog picked %s, want %s", path, tc.path)
			}
			want := "Config file: " + tc.path + " (" + tc.source + ")"
			lines, _ := ring.Since(0, 50)
			if len(lines) != 2 || !strings.Contains(lines[0].Text, "Starting fridolin ") || !strings.HasSuffix(lines[1].Text, want) {
				t.Errorf("logged %+v, want the version and then %q", lines, want)
			}
			if !strings.Contains(console.String(), want) {
				t.Errorf("the console does not say %q: %q", want, console.String())
			}
		})
	}
}

func TestRunLogsTheDataDir(t *testing.T) {
	// Without data_dir in the config the data dir, and with it the
	// WhatsApp session, depends on which folders exist; the log says which
	// one the bridge took, and why.
	tests := []struct {
		name    string
		config  string   // the config file; HOME stands for the home folder
		dirs    []string // folders in the home folder before it starts
		dataDir string   // relative to the home folder
		source  string
	}{
		{"set in the config", `{"data_dir": "~/data"}`, []string{".whatsapp-claude-bridge"}, "data", "config"},
		{"the default", `{}`, nil, ".fridolin", "default"},
		{"the folder from before the rename", `{}`, []string{".whatsapp-claude-bridge"}, ".whatsapp-claude-bridge", "legacy"},
		{"that folder named in the config", `{"data_dir": "HOME/.whatsapp-claude-bridge"}`, []string{".whatsapp-claude-bridge"}, ".whatsapp-claude-bridge", "config"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := fsx.Canonical(t.TempDir())
			t.Setenv("HOME", home)
			for _, d := range tc.dirs {
				if err := os.Mkdir(filepath.Join(home, d), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			logs := captureLog(t)
			cfgs, err := OpenConfigStore(writeConfig(t, strings.ReplaceAll(tc.config, "HOME", home)))
			if err != nil {
				t.Fatal(err)
			}
			logDataDir(cfgs)
			want := "Data dir: " + filepath.Join(home, tc.dataDir) + " (" + tc.source + ")"
			if !logContains(logs, want) {
				t.Errorf("the log does not say %q: %q", want, logs())
			}
		})
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
	if tmp := fsx.Canonical(os.TempDir()); !fsx.Within(fsx.Canonical(wd), tmp) {
		t.Errorf("working directory %s is not inside the temp dir %s", wd, tmp)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "go.mod")); err != nil {
		t.Errorf("repoDir %q is not the checkout: %v", repoDir, err)
	}
}

// The fuzz engine finds a target's committed inputs, and saves a failing
// one, under testdata/fuzz relative to the working directory. In the empty
// one the tests run in, that must still be the package's testdata.
func TestFuzzCorpusIsThePackagesTestdata(t *testing.T) {
	got, err := os.Readlink("testdata")
	if want := filepath.Join(pkgDir, "testdata"); err != nil || got != want {
		t.Errorf("testdata in the working directory links to %q (%v), want %q", got, err, want)
	}

	t.Run("reads and writes land in the package", func(t *testing.T) {
		pkg, wd := t.TempDir(), t.TempDir()
		corpus := filepath.Join(pkg, "testdata", "fuzz", "FuzzX")
		if err := os.MkdirAll(corpus, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(corpus, "seed"), []byte("go test fuzz v1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := testutil.LinkTestdata(pkg, wd); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(wd, "testdata", "fuzz", "FuzzX", "seed")); err != nil {
			t.Errorf("the committed input is not found from the working directory: %v", err)
		}
		// What the engine does with a failing input of a new target.
		crashDir := filepath.Join(wd, "testdata", "fuzz", "FuzzY")
		if err := os.MkdirAll(crashDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(crashDir, "crash"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(pkg, "testdata", "fuzz", "FuzzY", "crash")); err != nil {
			t.Errorf("a failing input saved from the working directory is not in the package: %v", err)
		}
	})

	t.Run("a package without testdata", func(t *testing.T) {
		pkg, wd := t.TempDir(), t.TempDir()
		if err := testutil.LinkTestdata(pkg, wd); err != nil {
			t.Fatalf("linking a missing testdata: %v", err)
		}
		if _, err := os.ReadDir(filepath.Join(wd, "testdata", "fuzz", "FuzzX")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("reading the corpus: %v, want it missing", err)
		}
		if err := os.MkdirAll(filepath.Join(wd, "testdata", "fuzz", "FuzzX"), 0o755); err == nil {
			t.Error("saving a failing input succeeded, so it went somewhere other than the package")
		}
	})
}

func TestTestsStartWithATempHome(t *testing.T) {
	// A test that forgets t.Setenv("HOME", ...) must find ~ and the default
	// data dir in a temp dir, not in the home of whoever runs the tests.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if tmp := fsx.Canonical(os.TempDir()); !fsx.Within(fsx.Canonical(home), tmp) {
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
	if os.Getenv("FRIDOLIN_TEST_CHECK_BRIDGE_VARIABLES_CLEARED") == "1" {
		for _, k := range testutil.BridgeVariables() {
			if _, ok := os.LookupEnv(k); ok {
				t.Errorf("%s from the shell reached the tests", k)
			}
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestShellBridgeVariablesDoNotReachTests$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), "FRIDOLIN_TEST_CHECK_BRIDGE_VARIABLES_CLEARED=1")
	notTheTests := filepath.Join(t.TempDir(), "not-the-tests")
	for _, k := range testutil.BridgeVariables() {
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

func TestNewConfigsHoldTheirDataDir(t *testing.T) {
	// A data folder given to setup.sh in its environment never reaches the
	// service, so it goes into the config. Without one the config gets the
	// folder the bridge picks now: left out, every start would pick it
	// again by which folders exist, and a ~/.fridolin made later by
	// something else would move a bridge off the folder from before the
	// rename. Written with ~, it holds under another home folder too. HOME
	// stands for the home folder.
	cases := []struct {
		name    string
		write   func(t *testing.T, path string) // writes a new config to path
		dirs    []string                        // folders in the home folder first
		raw     string                          // data_dir as written
		dataDir string                          // the data dir it loads as
	}{
		{"init", initWith(), nil, "~/.fridolin", "HOME/.fridolin"},
		{"init, before the rename", initWith(), []string{".whatsapp-claude-bridge"}, "~/.whatsapp-claude-bridge", "HOME/.whatsapp-claude-bridge"},
		{"init with both folders", initWith(), []string{".fridolin", ".whatsapp-claude-bridge"}, "~/.fridolin", "HOME/.fridolin"},
		{"init --data-dir", initWith("--data-dir", "HOME/elsewhere/data"), nil, "HOME/elsewhere/data", "HOME/elsewhere/data"},
		{"the first start", func(t *testing.T, path string) {
			if err := ensureConfigFile(path); err != nil {
				t.Fatal(err)
			}
		}, nil, "~/.fridolin", "HOME/.fridolin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := fsx.Canonical(t.TempDir())
			t.Setenv("HOME", home)
			expand := strings.NewReplacer("HOME", home).Replace
			for _, d := range c.dirs {
				if err := os.Mkdir(filepath.Join(home, d), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(home, "cfg", "config.json")
			c.write(t, path)
			var raw string
			if err := json.Unmarshal(readRawConfig(t, path)["data_dir"], &raw); err != nil || raw != expand(c.raw) {
				t.Errorf("data_dir written as %q (%v), want %q", raw, err, expand(c.raw))
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.DataDir != expand(c.dataDir) {
				t.Errorf("data_dir = %q, want %q", cfg.DataDir, expand(c.dataDir))
			}
		})
	}
}

// initWith returns a writer for TestNewConfigsHoldTheirDataDir that runs
// init with flags, where HOME stands for the home folder.
func initWith(flags ...string) func(t *testing.T, path string) {
	return func(t *testing.T, path string) {
		args := []string{"--config", path}
		home, _ := os.UserHomeDir()
		for _, f := range flags {
			args = append(args, strings.ReplaceAll(f, "HOME", home))
		}
		captureStdout(t, func() { initCmd(args) })
	}
}

// The run flags are the documented ones. --check (now the check command)
// and --dashboard-only (now --no-whatsapp) are refused like any other
// unknown flag, and -h prints the usage text with every command.
func TestRunFlags(t *testing.T) {
	cases := []struct {
		args    []string
		want    runOptions
		wantErr string // part of the error, "" for none
	}{
		{nil, runOptions{}, ""},
		{[]string{"--config", "/tmp/x.json", "--no-whatsapp", "--open"}, runOptions{config: "/tmp/x.json", noWhatsApp: true, open: true}, ""},
		{[]string{"--log-file", "/tmp/x.log"}, runOptions{logFile: "/tmp/x.log"}, ""},
		{[]string{"--check"}, runOptions{}, "flag provided but not defined: -check"},
		{[]string{"--config", "/tmp/x.json", "--check"}, runOptions{config: "/tmp/x.json"}, "flag provided but not defined: -check"},
		{[]string{"--dashboard-only"}, runOptions{}, "flag provided but not defined: -dashboard-only"},
		{[]string{"-h"}, runOptions{}, flag.ErrHelp.Error()},
		{[]string{"--help"}, runOptions{}, flag.ErrHelp.Error()},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			var out bytes.Buffer
			got, err := parseRunFlags(c.args, &out)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("error %v, want none", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("error %v, want one containing %q", err, c.wantErr)
			}
			if got != c.want {
				t.Errorf("options %+v, want %+v", got, c.want)
			}
			if c.wantErr != "" && !strings.Contains(out.String(), "fridolin check [--route JID ...]") {
				t.Errorf("printed %q, want the usage text", out.String())
			}
		})
	}
}

func TestPaths(t *testing.T) {
	// setup.sh asks the build for the config file and the data dir instead
	// of keeping its own copy of the rules, the legacy folder among them.
	// "home/" and "other/" stand for two temp dirs; the files and folders
	// listed are made first.
	tests := []struct {
		name        string
		flag, env   string // --config and FRIDOLIN_CONFIG; "" means none
		config      string // the config file's contents; "" means none
		dirs        []string
		want        resolvedPaths
		wantMissing []string // made by nothing, so still missing after
	}{
		{"a fresh install", "", "", "", nil,
			resolvedPaths{"home/.fridolin/config.json", "default", "home/.fridolin", "default"},
			[]string{"home/.fridolin"}},
		{"an install from before the rename", "", "", "", []string{"home/.whatsapp-claude-bridge/"},
			resolvedPaths{"home/.whatsapp-claude-bridge/config.json", "default", "home/.whatsapp-claude-bridge", "legacy"},
			[]string{"home/.fridolin"}},
		{"both folders", "", "", "", []string{"home/.fridolin/", "home/.whatsapp-claude-bridge/"},
			resolvedPaths{"home/.fridolin/config.json", "default", "home/.fridolin", "default"}, nil},
		{"FRIDOLIN_CONFIG with a data_dir", "", "other/env.json", `{"data_dir": "~/data"}`, nil,
			resolvedPaths{"other/env.json", "FRIDOLIN_CONFIG", "home/data", "config"},
			[]string{"home/data", "home/.fridolin"}},
		{"--config wins over FRIDOLIN_CONFIG", "other/flag.json", "other/env.json", `{"data_dir": "other/data"}`, nil,
			resolvedPaths{"other/flag.json", "--config", "other/data", "config"}, []string{"other/data"}},
		{"a config without data_dir", "other/flag.json", "", `{"allowed_numbers": []}`, nil,
			resolvedPaths{"other/flag.json", "--config", "home/.fridolin", "default"}, []string{"home/.fridolin"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home, other := fsx.Canonical(t.TempDir()), fsx.Canonical(t.TempDir())
			t.Setenv("HOME", home)
			abs := func(p string) string {
				if rest, ok := strings.CutPrefix(p, "home/"); ok {
					return filepath.Join(home, rest)
				}
				return filepath.Join(other, strings.TrimPrefix(p, "other/"))
			}
			testutil.ClearEnv(t, "FRIDOLIN_CONFIG", "CONFIG_FILE")
			if tc.env != "" {
				t.Setenv("FRIDOLIN_CONFIG", abs(tc.env))
			}
			for _, d := range tc.dirs {
				if err := os.MkdirAll(abs(d), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			var args []string
			if tc.flag != "" {
				args = []string{"--config", abs(tc.flag)}
			}
			if tc.config != "" {
				src := tc.flag
				if src == "" {
					src = tc.env
				}
				body := strings.ReplaceAll(tc.config, `"other/`, `"`+other+`/`)
				if err := os.WriteFile(abs(src), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want := resolvedPaths{abs(tc.want.Config), tc.want.ConfigSource, abs(tc.want.DataDir), tc.want.DataDirSource}

			var out, errs bytes.Buffer
			if code := pathsCmd(args, &out, &errs); code != 0 {
				t.Fatalf("exit %d: %s", code, errs.String())
			}
			wantLines := "config=" + want.Config + "\nconfig_source=" + want.ConfigSource +
				"\ndata_dir=" + want.DataDir + "\ndata_dir_source=" + want.DataDirSource + "\n"
			if out.String() != wantLines {
				t.Errorf("printed\n%s\nwant\n%s", out.String(), wantLines)
			}

			out.Reset()
			if code := pathsCmd(append(args, "--json"), &out, &errs); code != 0 {
				t.Fatalf("--json: exit %d: %s", code, errs.String())
			}
			var got resolvedPaths
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || got != want {
				t.Errorf("--json printed %s (%v), want %+v", out.String(), err, want)
			}
			for _, p := range tc.wantMissing {
				if _, err := os.Stat(abs(p)); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("%s exists after paths (%v), which must create nothing", abs(p), err)
				}
			}
		})
	}
}

func TestPathsAgreesWithTheLoadedConfig(t *testing.T) {
	// What paths reports is the data dir the bridge then runs on. HOME
	// stands for the test's home folder.
	for _, c := range []struct{ name, dataDir string }{
		{"no data_dir", ""},
		{"one under ~", "~/elsewhere"},
		{"an absolute one", "HOME/abs-data"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := fsx.Canonical(t.TempDir())
			t.Setenv("HOME", home)
			dataDir := strings.Replace(c.dataDir, "HOME", home, 1)
			path := writeConfig(t, `{"data_dir": "`+dataDir+`"}`)
			p, err := resolvePaths(path)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if p.DataDir != cfg.DataDir {
				t.Errorf("paths says %s, the bridge runs on %s", p.DataDir, cfg.DataDir)
			}
		})
	}
}

func TestPathsRefuses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	testutil.ClearEnv(t, "FRIDOLIN_CONFIG", "CONFIG_FILE")
	cases := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"a config that is no JSON", []string{"--config", writeConfig(t, `{"data_dir": `)}, 1, "parsing config file"},
		{"an argument", []string{"data_dir"}, 2, "takes no arguments"},
		{"an unknown flag", []string{"--data-dir", "x"}, 2, "flag provided but not defined"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errs bytes.Buffer
			if code := pathsCmd(c.args, &out, &errs); code != c.code || !strings.Contains(errs.String(), c.want) {
				t.Errorf("exit %d, stderr %q; want exit %d and %q", code, errs.String(), c.code, c.want)
			}
			if out.Len() > 0 {
				t.Errorf("printed %q on a refusal", out.String())
			}
		})
	}
}

func TestCheckRedact(t *testing.T) {
	// check's output goes into public bug reports. With --redact it shows
	// no configured number, JID or home folder, in the report, the log
	// lines of loading the config and the sandbox profile alike; without
	// it, it shows them as they are. The home folder has a dot in its
	// name, which a sandbox profile's regular expressions write escaped.
	home := filepath.Join(fsx.Canonical(t.TempDir()), "first.last")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	testutil.ClearEnv(t, "FRIDOLIN_CONFIG", "CONFIG_FILE")
	// otherWritten has a note after the number, which makes it no number
	// to the bridge, and oddGroup has no @g.us, so loading the config
	// logs both as they are written.
	const (
		owner, other  = "15551234567", "15559876543"
		otherWritten  = "+1 555 987 6543 (mobile)"
		oddGroup      = "120363000000000004"
		allowedGroup  = "120363000000000001@g.us"
		brainGroup    = "120363000000000002@g.us"
		ownerJID      = owner + "@s.whatsapp.net"
		strangerGroup = "120363000000000003@g.us"
		// memberOnly is in group_members only, which check prints as
		// digits, with no + or JID that would give it away as a number.
		memberOnly = "15550001234" // personal-data:ignore (a fictional 555 number)
	)
	lid := strings.Repeat("7", 15) + "@lid"
	path := filepath.Join(home, ".fridolin", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{
		"claude_path": "` + fakeClaudeEnv(t, 0) + `",
		"data_dir": "~/.fridolin",
		"allowed_numbers": ["` + owner + `", "` + otherWritten + `"],
		"allowed_groups": ["` + allowedGroup + `", "` + oddGroup + `"],
		"group_members": {"` + allowedGroup + `": ["` + memberOnly + `"]},
		"not_a_setting": true` + brainDirsJSON(t,
		brainFolder(t, home+"/brains/main", manifest.Manifest{ID: "main", Routing: manifest.Routing{HandlesDMs: true, Groups: []string{brainGroup}}}),
	) + `}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	o := checkOptions{routes: []string{ownerJID, allowedGroup, strangerGroup, lid}, sandboxFor: "main"}
	if runtime.GOOS != "darwin" {
		o.sandboxFor = "" // the profile is sandbox-exec's
	}

	report := func(t *testing.T, redact bool) (string, []string) {
		t.Helper()
		logs := captureLog(t)
		o.redact = redact
		var out bytes.Buffer
		if err := writeCheck(&out, o); err != nil {
			t.Fatal(err)
		}
		return out.String(), logs()
	}
	secrets := []string{home, fsx.Resolve(home), regexp.QuoteMeta(home), owner, other, otherWritten, oddGroup, memberOnly,
		"120363000000000001", "120363000000000002", "120363000000000003", strings.Repeat("7", 15)}

	t.Run("redacted", func(t *testing.T) {
		out, logs := report(t, true)
		all := out + strings.Join(logs, "\n")
		for _, s := range secrets {
			if strings.Contains(all, s) {
				t.Errorf("%q is in the redacted output:\n%s", s, all)
			}
		}
		want := []string{
			"Config OK: ~/.fridolin/config.json\n",
			"  data dir:  ~/.fridolin\n",
			"  DM allow:  [1…67 +1…43]\n",
			"  groups:    […01@g.us …04]\n",
			"  heard in …01@g.us: [1…34] (group_members)\n",
			"  heard in …04: [1…67 +1…43] (no group_members entry: allowed_numbers)\n",
			"→ ~/brains/main (dms=true default=false groups=[…02@g.us])\n",
			"  route 1…67@s.whatsapp.net → main\n",
			"  route …01@g.us → ",
			"  route …03@g.us → main (not in allowed_groups: ignored)\n",
			"  route …77@lid → ",
		}
		if o.sandboxFor != "" {
			want = append(want, `"~/.fridolin"`, `"^~/`)
		}
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("no %q in:\n%s", w, out)
			}
		}
		for _, w := range []string{"config ~/.fridolin/config.json: ignoring unknown settings", `allowed_numbers "+1…43" (digits with the country code)`, `allowed_groups "…04"`} {
			if !logContains(func() []string { return logs }, w) {
				t.Errorf("the log of loading the config does not say %q: %q", w, logs)
			}
		}
	})

	t.Run("as it is", func(t *testing.T) {
		out, logs := report(t, false)
		for _, s := range append([]string{home}, owner, otherWritten, oddGroup, allowedGroup, brainGroup, strangerGroup, lid, memberOnly, "Config OK: "+path+"\n", "  DM allow:  ["+owner+" "+otherWritten+"]\n") {
			if !strings.Contains(out, s) {
				t.Errorf("no %q in:\n%s", s, out)
			}
		}
		if strings.Contains(out, "…") || strings.Contains(out, "~/") {
			t.Errorf("the output is redacted without --redact:\n%s", out)
		}
		if !logContains(func() []string { return logs }, "config "+path+": ignoring unknown settings") {
			t.Errorf("the log of loading the config changed: %q", logs)
		}
		// The same report, only masked.
		red, _ := report(t, true)
		if got, want := strings.Count(red, "\n"), strings.Count(out, "\n"); got != want {
			t.Errorf("the redacted report has %d lines, the plain one %d", got, want)
		}
	})

	// The chats of the config file, read before it loads (addConfig), are
	// masked as written: loading logs what it finds odd that way. No such
	// log names a group_members number today, so only this case needs
	// addConfig to add them; the report masks the loaded ones.
	t.Run("as written", func(t *testing.T) {
		r := newRedactor(home)
		r.addConfig(path)
		for _, s := range []string{owner, otherWritten, oddGroup, allowedGroup, memberOnly} {
			if got := r.text(s); strings.Contains(got, s) {
				t.Errorf("%q, from the config file, is redacted to %q", s, got)
			}
		}
	})

	t.Run("an error", func(t *testing.T) {
		broken := filepath.Join(home, "broken", "config.json")
		makeFiles(t, home, "broken/config.json")
		for _, redact := range []bool{true, false} {
			captureLog(t)
			err := writeCheck(io.Discard, checkOptions{config: broken, redact: redact})
			if err == nil {
				t.Fatal("a config that is no JSON loaded")
			}
			if got := strings.Contains(err.Error(), home); got == redact {
				t.Errorf("redact=%v: the error names the home folder: %v", redact, got)
			}
		}
	})
}

func TestRedactorText(t *testing.T) {
	// Made-up numbers outside the placeholders' country carry
	// personal-data:ignore.
	r := newRedactor("/srv/first.last")
	r.addChats("15551234567", "+15559876543", "15559876543@s.whatsapp.net")
	r.addGroups("120363000000000001@g.us", "120363000000000009")
	cases := []struct{ in, want string }{
		{"15551234567@s.whatsapp.net", "1…67@s.whatsapp.net"},
		{"15551234567:12@s.whatsapp.net", "1…67@s.whatsapp.net"},           // a device's JID
		{"491701234567@s.whatsapp.net", "49…67@s.whatsapp.net"},            // a two-digit country code; personal-data:ignore
		{"3530000000099@s.whatsapp.net", "353…99@s.whatsapp.net"},          // a three-digit one; personal-data:ignore
		{"120363000000000001@g.us", "…01@g.us"},                            // a group
		{"15551234567-1600000000@g.us", "…00@g.us"},                        // a group named by its creator's number; personal-data:ignore
		{strings.Repeat("7", 15) + "@lid", "…77@lid"},                      // a LID
		{"[15551234567 15559876543]", "[1…67 1…43]"},                       // numbers configured or routed
		{`"120363000000000009" (digits@g.us)`, `"…09" (digits@g.us)`},      // a group written without @g.us
		{"+15559876543 and +4917012345678", "+1…43 and +49…78"},            // international format; personal-data:ignore
		{"you@example.com", "y…@example.com"},                              // as maskEmail
		{"/srv/first.last/.fridolin", "~/.fridolin"},                       // a path
		{`(subpath "/srv/first.last")`, `(subpath "~")`},                   // the folder itself
		{`(regex "^/srv/first\\.last/x(/.*)?$")`, `(regex "^~/x(/.*)?$")`}, // in a sandbox profile's regular expression
		{"/srv/first.lastname/x", "/srv/first.lastname/x"},                 // another folder
		{"127.0.0.1:8765 and 2.1.400", "127.0.0.1:8765 and 2.1.400"},       // what is no number
		{"12345@g.us", "…@g.us"},                                           // too short to keep any digit
		{"allowed_groups (digits@g.us)", "allowed_groups (digits@g.us)"},   // no JID, and no address either
		{"brain-120363000000000005@g.us", "brain-…05@g.us"},                // a JID right after a name
		{"main.120363000000000005@g.us", "main.…05@g.us"},                  // or a word
		{"x15550001111@s.whatsapp.net", "x1…11@s.whatsapp.net"},            // a number's; personal-data:ignore
		{"x" + strings.Repeat("6", 15) + "@lid", "x…66@lid"},               // a LID's
	}
	for _, c := range cases {
		if got := r.text(c.in); got != c.want {
			t.Errorf("text(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := newRedactor("/").text("/usr/bin"); got != "/usr/bin" {
		t.Errorf("a home of / redacted %q", got)
	}
}

// TestCheckExitsWhenABrainCannotRun: check printed an approved brain it
// would leave out as an issue and exited 0, and setup.sh keeps only its
// claude line, so a DM brain left out went unnoticed while its DMs went
// to the default brain. check now exits 3 for such a brain, still 0 for
// one that only drifted, and 1 for a config that does not load.
func TestCheckExitsWhenABrainCannotRun(t *testing.T) {
	if path := os.Getenv("FRIDOLIN_TEST_CHECK"); path != "" {
		args := []string{"--config", path}
		if jid := os.Getenv("FRIDOLIN_TEST_CHECK_ROUTE"); jid != "" {
			args = append(args, "--route", jid)
		}
		if os.Getenv("FRIDOLIN_TEST_CHECK_REDACT") != "" {
			args = append(args, "--redact")
		}
		checkCmd(args)
		os.Exit(0)
	}
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	// No CLI to ask for its version: the fake would take the place of
	// the test in the process check runs in.
	data := home + `/data", "claude_path": "` + home + `/no-claude`
	main := brainFolder(t, home+"/main", manifest.Manifest{ID: "main", Routing: manifest.Routing{HandlesDMs: true}})
	drifted := brainFolder(t, home+"/notes", manifest.Manifest{ID: "notes"})
	renamed := *drifted.Approved
	renamed.Name = "Notes, approved before"
	drifted.Approved = &renamed
	inData := brainFolder(t, home+"/data/inside", manifest.Manifest{ID: "inside"})
	gone := brainDirsJSON(t, main, config.BrainRef{Path: home + "/gone", Approved: &manifest.Manifest{ID: "gone", Name: "gone"}})
	cases := []struct {
		name   string
		config string
		route  string // for --route
		redact bool   // for --redact
		exit   int
		says   string // on stderr
	}{
		{"every brain runs", `{"brain_mode": true, "data_dir": "` + data + `"` + brainDirsJSON(t, main) + `}`, "", false, 0, ""},
		{"a brain drifted", `{"brain_mode": true, "data_dir": "` + data + `"` + brainDirsJSON(t, main, drifted) + `}`, "", false, 0, ""},
		{"a brain in the data folder", `{"brain_mode": true, "data_dir": "` + data + `"` + brainDirsJSON(t, main, inData) + `}`, "", false, checkBrainsLeftOut, "brain inside cannot run"},
		{"a brain in the data folder, with a route", `{"brain_mode": true, "data_dir": "` + data + `"` + brainDirsJSON(t, main, inData) + `}`, "15551234567@s.whatsapp.net", false, checkBrainsLeftOut, "brain inside cannot run"},
		{"a brain whose folder is gone", `{"brain_mode": true, "data_dir": "` + data + `"` + gone + `}`, "", false, checkBrainsLeftOut, "brain gone cannot run"},
		// --redact used to make every error a new one, and check then
		// exited 1 for a brain that cannot run.
		{"a brain whose folder is gone, redacted", `{"brain_mode": true, "data_dir": "` + data + `"` + gone + `}`, "", true, checkBrainsLeftOut, "brain gone cannot run"},
		{"a config that does not load", `{"brain_mode": true, "brains": [{"id": "old"}]}`, "", false, 1, ""},
		{"a config that does not load, redacted", `{"brain_mode": true, "brains": [{"id": "old"}]}`, "", true, 1, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestCheckExitsWhenABrainCannotRun$", "-test.count=1")
			cmd.Env = append(os.Environ(), "FRIDOLIN_TEST_CHECK="+writeConfig(t, c.config), "FRIDOLIN_TEST_CHECK_ROUTE="+c.route)
			if c.redact {
				cmd.Env = append(cmd.Env, "FRIDOLIN_TEST_CHECK_REDACT=1")
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			exit := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exit = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if exit != c.exit {
				t.Errorf("check exited %d, want %d:\n%s", exit, c.exit, stderr.String())
			}
			if !strings.Contains(stderr.String(), c.says) {
				t.Errorf("check said %q, want %q", stderr.String(), c.says)
			}
		})
	}
}
