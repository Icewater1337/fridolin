package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/version"
)

// fakeService is a serviceHost whose launchctl, systemctl and HTTP calls
// go to the test: nothing reaches the real service manager or a bridge.
type fakeService struct {
	t      *testing.T
	h      *serviceHost
	home   string
	cfg    string // config path
	src    string // a source checkout
	cmds   []string
	out    *strings.Builder
	loaded bool // what launchctl print answers

	health int                  // /health status; 0 refuses the connection
	ready  func() (int, []byte) // /ready
	clock  time.Time            // what h.now says; h.sleep moves it on
}

func newFakeService(t *testing.T, goos string) *fakeService {
	t.Helper()
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	f := &fakeService{t: t, home: home, out: &strings.Builder{}, health: 200, clock: time.Now()}
	f.ready = func() (int, []byte) {
		data, _ := json.Marshal(httpapi.ReadyResponse{Ready: false, WhatsApp: "pairing", Version: version.String(), StartedAt: time.Now()})
		return 503, data
	}
	f.cfg = writeConfig(t, `{"brain_mode": true, "data_dir": "`+home+`/.fridolin", "http_listen": "127.0.0.1:18765"}`)
	f.src = home + "/src/fridolin"
	makeFiles(t, f.src, "go.mod", "tools/requirements.txt", "tools/tests/test_poll.py", "tools/README.md", "bin/fridolin")
	if err := os.WriteFile(f.src+"/tools/poll_results.py", []byte("print('poll')\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.src+"/tools/requirements.txt", 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../go.mod", f.src+"/tools/linked.py"); err != nil {
		t.Fatal(err)
	}
	f.h = &serviceHost{
		goos: goos,
		home: home,
		uid:  501,
		path: home + "/bin:relative/bin:/no/such/dir:/usr/bin",
		out:  f.out,
		run: func(name string, args ...string) (string, error) {
			cmd := name + " " + strings.Join(args, " ")
			f.cmds = append(f.cmds, cmd)
			switch {
			case strings.HasPrefix(cmd, "launchctl print"):
				if !f.loaded {
					return "Could not find service", errors.New("exit status 113")
				}
				return "\tstate = running\n\tpid = 4242\n", nil
			case strings.HasPrefix(cmd, "launchctl bootstrap"):
				f.loaded = true
			case strings.HasPrefix(cmd, "launchctl bootout"):
				f.loaded = false
			case strings.HasPrefix(cmd, "systemctl --user is-active"):
				return "active\n", nil
			case name == "plutil":
				// The real one, where there is one: it only converts
				// the test's own files.
				out, err := exec.Command(name, args...).CombinedOutput()
				return string(out), err
			}
			return "", nil
		},
		get: func(url string) (int, []byte, error) {
			switch {
			case strings.HasSuffix(url, "/health") && f.health != 0:
				return f.health, []byte("ok\n"), nil
			case strings.HasSuffix(url, "/ready") && f.health != 0:
				code, body := f.ready()
				return code, body, nil
			}
			return 0, nil, errors.New("connection refused")
		},
		sleep: func(d time.Duration) { f.clock = f.clock.Add(d) },
		now:   func() time.Time { return f.clock },
		wait:  2 * time.Second,
	}
	return f
}

// exe writes a bridge binary with the given content and returns its path.
func (f *fakeService) exe(content string) string {
	f.t.Helper()
	p := filepath.Join(f.t.TempDir(), "fridolin")
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fakeService) dir() string { return defaultInstallDir(f.home) }

func (f *fakeService) install(exe string, mutate ...func(*installOptions)) error {
	o := installOptions{ConfigPath: f.cfg, Dir: f.dir(), Exe: exe, Source: f.src}
	for _, m := range mutate {
		m(&o)
	}
	f.cmds = nil
	f.out.Reset()
	return f.h.install(o)
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mode(t *testing.T, p string) fs.FileMode {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

// awkward holds the characters that broke the sed templating: '|' and
// '&' in sed, '&' and '<' in XML, and '%', '$', '"' and spaces for systemd.
const awkward = `/Users/you/A & B <x> "q" 'p' 100% $HOME|ü`

func TestRenderLaunchdAgentEscapesEveryValue(t *testing.T) {
	d := unitData{
		Label:   serviceLabel,
		Binary:  awkward + "/fridolin",
		Config:  awkward + "/config.json",
		WorkDir: awkward + "/data",
		LogDir:  awkward + "/logs",
		Env: []envVar{
			{"HOME", awkward}, {"PATH", awkward + "/bin:/usr/bin"}, {"GROQ_API_KEY", `k&<>"'`},
			// Kept from an agent edited by hand, with the value on
			// lines of its own.
			{"CLAUDE_CODE_OAUTH_TOKEN", "\n\t token\r\n    "},
		},
	}
	unit, err := renderUnit("darwin", d)
	if err != nil {
		t.Fatal(err)
	}
	u, err := readUnit("darwin", unit)
	if err != nil {
		t.Fatalf("the plist does not parse: %v\n%s", err, unit)
	}
	if u.Binary != d.Binary {
		t.Errorf("ProgramArguments[0] = %q, want %q", u.Binary, d.Binary)
	}
	for _, e := range d.Env {
		if u.Env[e.Key] != e.Value {
			t.Errorf("%s = %q, want %q", e.Key, u.Env[e.Key], e.Value)
		}
	}
	v, _ := parsePlist(unit)
	root := v.(map[string]any)
	if root["WorkingDirectory"] != d.WorkDir || root["StandardErrorPath"] != d.LogDir+"/stderr.log" || root["Label"] != serviceLabel {
		t.Errorf("plist = %v", root)
	}
	if args, _ := root["ProgramArguments"].([]any); len(args) != 5 || args[3] != "--log-file" || args[4] != d.LogDir+"/stderr.log" {
		t.Errorf("ProgramArguments = %q, want the log file passed to the bridge", args)
	}
	if root["KeepAlive"] != true || root["RunAtLoad"] != true {
		t.Errorf("KeepAlive/RunAtLoad = %v/%v, want true", root["KeepAlive"], root["RunAtLoad"])
	}

	// macOS's own parser agrees.
	if plutil, err := exec.LookPath("plutil"); err == nil {
		p := filepath.Join(t.TempDir(), "agent.plist")
		if err := os.WriteFile(p, unit, 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(plutil, "-lint", p).CombinedOutput(); err != nil {
			t.Errorf("plutil -lint: %v: %s", err, out)
		}
		for key, want := range map[string]string{"ProgramArguments.0": d.Binary, "ProgramArguments.2": d.Config, "EnvironmentVariables.PATH": d.Env[1].Value, "EnvironmentVariables.CLAUDE_CODE_OAUTH_TOKEN": d.Env[3].Value} {
			out, err := exec.Command(plutil, "-extract", key, "raw", "-o", "-", p).Output()
			if got := strings.TrimSuffix(string(out), "\n"); err != nil || got != want {
				t.Errorf("plutil %s = %q (%v), want %q", key, got, err, want)
			}
		}
	}
}

func TestRenderSystemdUnitEscapesEveryValue(t *testing.T) {
	d := unitData{
		Label:   serviceLabel,
		Binary:  awkward + `/back\slash/fridolin`,
		Config:  awkward + "/config.json",
		WorkDir: awkward + "/data",
		Env:     []envVar{{"HOME", awkward}, {"PATH", awkward + "/bin:/usr/bin"}},
	}
	unit, err := renderUnit("linux", d)
	if err != nil {
		t.Fatal(err)
	}
	text := string(unit)
	const quoted = `"/Users/you/A & B <x> \"q\" 'p' 100%% `
	for _, want := range []string{
		"ExecStart=" + quoted + `$$HOME|ü/back\\slash/fridolin" --config ` + quoted + `$$HOME|ü/config.json"` + "\n",
		"WorkingDirectory=/Users/you/A & B <x> \"q\" 'p' 100%% $HOME|ü/data\n",
		// Environment= expands specifiers but not $VAR.
		`Environment="HOME=/Users/you/A & B <x> \"q\" 'p' 100%% $HOME|ü"` + "\n",
		`Environment="PATH=/Users/you/A & B <x> \"q\" 'p' 100%% $HOME|ü/bin:/usr/bin"` + "\n",
		"Restart=on-failure\n",
		"NoNewPrivileges=yes\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("unit lacks %q:\n%s", want, text)
		}
	}
	// A user unit cannot order itself after the system's network target.
	if strings.Contains(text, "network-online") {
		t.Errorf("unit still waits for network-online.target:\n%s", text)
	}
	if u, _ := readUnit("linux", unit); u.Binary != d.Binary {
		t.Errorf("ExecStart reads back as %q, want %q", u.Binary, d.Binary)
	}
}

// TestGoldenServiceUnits pins the launchd agent and the systemd unit that
// "fridolin service install" writes for a usual install, so that a change
// to either shows in review.
func TestGoldenServiceUnits(t *testing.T) {
	for _, tc := range []struct{ goos, golden string }{
		{"darwin", "service/launchd.plist"},
		{"linux", "service/fridolin.service"},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			d := unitData{
				Label:   serviceLabel,
				Binary:  "/Users/you/.local/lib/fridolin/fridolin",
				Config:  "/Users/you/.fridolin/config.json",
				WorkDir: "/Users/you/.fridolin",
				LogDir:  "/Users/you/Library/Logs/fridolin",
				Env:     []envVar{{"HOME", "/Users/you"}, {"PATH", "/Users/you/.local/bin:/opt/homebrew/bin:/usr/bin:/bin"}},
			}
			if tc.goos == "darwin" {
				// The personal-data check reads the placeholder home
				// folder and the plist's closing tag after it as one path;
				// a / ends it.
				d.Env[0].Value = "/Users/you/"
				// An agent keeps what was added to it, a key for a tool say.
				d.Env = append(d.Env, envVar{"GROQ_API_KEY", "<KEY>"})
			}
			unit, err := renderUnit(tc.goos, d)
			if err != nil {
				t.Fatal(err)
			}
			assertGolden(t, tc.golden, unit)
		})
	}
}

func TestRenderUnitRefusesWhatItCannotHold(t *testing.T) {
	ok := unitData{Label: serviceLabel, Binary: "/b/fridolin", Config: "/c/config.json", WorkDir: "/d", LogDir: "/l"}
	// A variable's value may be a credential kept from the agent: the
	// refusal names the variable and never shows the value.
	const secret = "FAKE-TOKEN-VALUE-123"
	cases := []struct {
		name   string
		goos   string
		mutate func(*unitData)
		named  string // what the refusal must name, if anything
	}{
		{"newline in the binary path", "darwin", func(d *unitData) { d.Binary = "/b\n/fridolin" }, ""},
		{"newline in the binary path", "linux", func(d *unitData) { d.Binary = "/b\n/fridolin" }, ""},
		{"control character in a variable", "darwin", func(d *unitData) { d.Env = []envVar{{"GROQ_API_KEY", secret + "\x01"}} }, "GROQ_API_KEY"},
		{"newline in a variable", "linux", func(d *unitData) { d.Env = []envVar{{"GROQ_API_KEY", "\n  " + secret + "\n"}} }, "GROQ_API_KEY"},
		{"newline in a variable's name", "darwin", func(d *unitData) { d.Env = []envVar{{"GROQ\nKEY", secret}} }, `"GROQ\nKEY"`},
		{"backslash in the data folder", "linux", func(d *unitData) { d.WorkDir = `/d\x` }, ""},
		{"trailing space in the data folder", "linux", func(d *unitData) { d.WorkDir = "/d " }, ""},
	}
	for _, c := range cases {
		t.Run(c.goos+" "+c.name, func(t *testing.T) {
			d := ok
			c.mutate(&d)
			_, err := renderUnit(c.goos, d)
			if err == nil {
				t.Fatalf("rendered %+v", d)
			}
			if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), c.named) {
				t.Errorf("err = %q, want it to name %s and not show the value", err, c.named)
			}
		})
	}
	for _, goos := range []string{"darwin", "linux"} {
		if _, err := renderUnit(goos, ok); err != nil {
			t.Errorf("%s: %v", goos, err)
		}
	}
}

// TestServicePath: the service runs with the installer's PATH, then the
// usual folders. A folder of it inside a brain's folder (a bin/ with the
// scripts that brain's prompt runs by name) went into the service's PATH
// too, where the bridge ran programs from it unsandboxed, and the sandbox
// then refused the brain. It now goes into brainPATHVar instead, by the
// brain folder as configured or by its real path, with what a bridge that
// installs itself kept there.
func TestServicePath(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	makeFiles(t, home, "bin/", "tools/bin/", "brains/p/bin/", "brains/p/tools/", "real/q/bin/", "kept/bin/")
	if err := os.Symlink(home+"/real/q", home+"/q"); err != nil {
		t.Fatal(err)
	}
	owners := []pathOwner{
		{id: "p", dirs: []string{home + "/brains/p"}},
		{id: "q", dirs: []string{home + "/q", home + "/real/q"}},
	}
	installer := strings.Join([]string{home + "/brains/p/bin", home + "/bin", "relative/bin", "", home + "/missing", home + "/tools/bin", home + "/bin", "/usr/bin", home + "/q/bin", home + "/real/q/bin", home + "/brains/p/tools/"}, ":")
	cases := []struct {
		name, goos      string
		owners          []pathOwner
		installerBrain  string
		want, wantBrain []string
	}{
		{"linux", "linux", nil, "", []string{home + "/brains/p/bin", home + "/bin", home + "/tools/bin", "/usr/bin", home + "/q/bin", home + "/real/q/bin", home + "/brains/p/tools", home + "/.local/bin", "/usr/local/bin", "/bin", "/usr/sbin", "/sbin"}, nil},
		{"darwin", "darwin", nil, "", []string{home + "/brains/p/bin", home + "/bin", home + "/tools/bin", "/usr/bin", home + "/q/bin", home + "/real/q/bin", home + "/brains/p/tools", "/opt/homebrew/bin", home + "/.local/bin", "/usr/local/bin", "/bin", "/usr/sbin", "/sbin"}, nil},
		{"brain folders", "darwin", owners, "",
			[]string{home + "/bin", home + "/tools/bin", "/usr/bin", "/opt/homebrew/bin", home + "/.local/bin", "/usr/local/bin", "/bin", "/usr/sbin", "/sbin"},
			[]string{home + "/brains/p/bin", home + "/q/bin", home + "/real/q/bin", home + "/brains/p/tools"}},
		{"what a bridge installing itself kept for brains", "darwin", owners, home + "/kept/bin:" + home + "/missing",
			[]string{home + "/bin", home + "/tools/bin", "/usr/bin", "/opt/homebrew/bin", home + "/.local/bin", "/usr/local/bin", "/bin", "/usr/sbin", "/sbin"},
			[]string{home + "/kept/bin", home + "/brains/p/bin", home + "/q/bin", home + "/real/q/bin", home + "/brains/p/tools"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, gotBrain := servicePath(installer, c.installerBrain, home, c.goos, c.owners)
			if got != strings.Join(c.want, ":") {
				t.Errorf("servicePath =\n  %s\nwant\n  %s", got, strings.Join(c.want, ":"))
			}
			if gotBrain != strings.Join(c.wantBrain, ":") {
				t.Errorf("brain PATH =\n  %s\nwant\n  %s", gotBrain, strings.Join(c.wantBrain, ":"))
			}
		})
	}
}

// TestServiceInstallKeepsBrainFoldersOffThePATH: installed from a shell
// whose PATH names a folder in a brain's folder, the service ran with it
// on PATH, so the bridge ran programs a brain could write there outside
// the sandbox. The folder now goes into the unit's brainPATHVar instead,
// for that brain's runs, and a later install without one drops the
// variable rather than keeping it as one you added.
func TestServiceInstallKeepsBrainFoldersOffThePATH(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			f := newFakeService(t, goos)
			makeFiles(t, f.home, "bin/", "assistant/personal/bin/")
			ref := brainFolder(t, f.home+"/assistant/personal", manifest.Manifest{ID: "personal", Routing: manifest.Routing{HandlesDMs: true}})
			f.cfg = writeConfig(t, `{"brain_mode": true, "data_dir": "`+f.home+`/.fridolin", "http_listen": "127.0.0.1:18765"`+brainDirsJSON(t, ref)+`}`)
			f.h.path = f.home + "/assistant/personal/bin:" + f.home + "/bin:/usr/bin"
			unitPath, _ := f.h.unitFile()
			env := func() (path, brainPath string) {
				t.Helper()
				data := readFile(t, unitPath)
				if goos == "linux" {
					for _, line := range strings.Split(data, "\n") {
						if v, ok := strings.CutPrefix(line, `Environment="PATH=`); ok {
							path = strings.TrimSuffix(v, `"`)
						}
						if v, ok := strings.CutPrefix(line, `Environment="`+brainPATHVar+`=`); ok {
							brainPath = strings.TrimSuffix(v, `"`)
						}
					}
					return path, brainPath
				}
				u, err := readUnit(goos, []byte(data))
				if err != nil {
					t.Fatal(err)
				}
				return u.Env["PATH"], u.Env[brainPATHVar]
			}
			if err := f.install(f.exe("build one")); err != nil {
				t.Fatalf("%v\n%s", err, f.out)
			}
			path, brainPath := env()
			if strings.Contains(path, "/assistant/personal/") || !strings.HasPrefix(path, f.home+"/bin:/usr/bin") {
				t.Errorf("PATH = %q, want it without the brain's folder", path)
			}
			if brainPath != f.home+"/assistant/personal/bin" {
				t.Errorf("%s = %q, want the brain's folder", brainPATHVar, brainPath)
			}
			f.cfg = writeConfig(t, `{"brain_mode": true, "data_dir": "`+f.home+`/.fridolin", "http_listen": "127.0.0.1:18765"}`)
			f.h.path = f.home + "/bin:/usr/bin"
			if err := f.install(f.exe("build two")); err != nil {
				t.Fatalf("%v\n%s", err, f.out)
			}
			if _, brainPath := env(); brainPath != "" {
				t.Errorf("%s = %q after an install without brain folders on PATH, want it gone", brainPATHVar, brainPath)
			}
		})
	}
}

func TestServiceInstallRunsACopyOutsideTheCheckout(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			f := newFakeService(t, goos)
			dir := f.dir()
			unitPath, _ := f.h.unitFile()

			if err := f.install(f.exe("build one")); err != nil {
				t.Fatalf("%v\n%s", err, f.out)
			}
			bin := filepath.Join(dir, "fridolin")
			if readFile(t, bin) != "build one" || mode(t, bin) != 0o755 {
				t.Errorf("installed binary %q, mode %v", readFile(t, bin), mode(t, bin))
			}
			if pathExists(bin + ".prev") {
				t.Errorf("a first install made fridolin.prev")
			}
			// The helpers and their requirements; not the tests, the
			// README or a symlink back into the checkout.
			tools, _ := os.ReadDir(filepath.Join(dir, "tools"))
			var names []string
			for _, e := range tools {
				names = append(names, e.Name())
			}
			if !slices.Equal(names, []string{"poll_results.py", "requirements.txt"}) {
				t.Errorf("tools = %v", names)
			}
			if mode(t, filepath.Join(dir, "tools", "poll_results.py")) != 0o755 || mode(t, filepath.Join(dir, "tools", "requirements.txt")) != 0o644 {
				t.Errorf("tool modes %v %v", mode(t, filepath.Join(dir, "tools", "poll_results.py")), mode(t, filepath.Join(dir, "tools", "requirements.txt")))
			}
			m, err := readInstallManifest(dir)
			if err != nil || m.Source != f.src || m.Version != version.String() || m.InstalledAt.IsZero() {
				t.Errorf("install.json = %+v, %v", m, err)
			}
			// The installed binary finds its tools and the checkout.
			if l := layoutOf(bin); l != (bridgeLayout{Dir: dir, Tools: dir + "/tools", Source: f.src}) {
				t.Errorf("layoutOf(installed) = %+v", l)
			}

			unit := readFile(t, unitPath)
			u, err := readUnit(goos, []byte(unit))
			if err != nil || u.Binary != bin {
				t.Errorf("the unit runs %q (%v), want %s:\n%s", u.Binary, err, bin, unit)
			}
			if mode(t, unitPath) != 0o600 {
				t.Errorf("unit mode %v, want 0600", mode(t, unitPath))
			}
			if !strings.Contains(unit, f.home+"/.local/bin") || !strings.Contains(unit, f.home+"/.fridolin") || !strings.Contains(unit, f.cfg) {
				t.Errorf("unit lacks the PATH, data folder or config:\n%s", unit)
			}
			want := []string{"launchctl print gui/501/" + serviceLabel, "launchctl bootstrap gui/501 " + unitPath}
			if goos == "linux" {
				want = []string{"systemctl --user daemon-reload", "systemctl --user enable fridolin.service", "systemctl --user restart fridolin.service"}
			}
			if !slices.Equal(f.cmds, want) {
				t.Errorf("ran %q, want %q", f.cmds, want)
			}
			if goos == "darwin" && mode(t, f.home+"/Library/Logs/fridolin") != 0o700 {
				t.Errorf("log folder mode %v, want 0700", mode(t, f.home+"/Library/Logs/fridolin"))
			}
			if !strings.Contains(f.out.String(), "the bridge is running ("+version.String()+")") || !strings.Contains(f.out.String(), "pair it in the dashboard") {
				t.Errorf("output:\n%s", f.out)
			}

			// An upgrade keeps the previous binary, tools and record, and
			// restarts the running service, which `enable --now` did not.
			if err := os.WriteFile(f.src+"/tools/poll_results.py", []byte("print('poll 2')\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := f.install(f.exe("build two")); err != nil {
				t.Fatalf("%v\n%s", err, f.out)
			}
			if readFile(t, bin) != "build two" || readFile(t, bin+".prev") != "build one" {
				t.Errorf("after the upgrade: fridolin %q, fridolin.prev %q", readFile(t, bin), readFile(t, bin+".prev"))
			}
			if readFile(t, dir+"/tools/poll_results.py") != "print('poll 2')\n" || readFile(t, dir+"/tools.prev/poll_results.py") != "print('poll')\n" {
				t.Errorf("tools not rotated")
			}
			if !pathExists(dir + "/install.prev.json") {
				t.Errorf("no install.prev.json")
			}
			want = []string{"launchctl print gui/501/" + serviceLabel, "launchctl bootout gui/501/" + serviceLabel, "launchctl bootstrap gui/501 " + unitPath}
			if goos == "linux" {
				want = []string{"systemctl --user daemon-reload", "systemctl --user enable fridolin.service", "systemctl --user restart fridolin.service"}
			}
			if !slices.Equal(f.cmds, want) {
				t.Errorf("upgrade ran %q, want %q", f.cmds, want)
			}

			// The same build again changes nothing it keeps for a rollback.
			if err := f.install(f.exe("build two")); err != nil {
				t.Fatalf("%v\n%s", err, f.out)
			}
			if readFile(t, bin+".prev") != "build one" || readFile(t, dir+"/tools.prev/poll_results.py") != "print('poll')\n" {
				t.Errorf("reinstalling the same build rotated the previous install")
			}
			if !strings.Contains(f.out.String(), "already this build") {
				t.Errorf("output:\n%s", f.out)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != len(installEntries) {
				t.Errorf("install folder holds %d entries, want %d: %v", len(entries), len(installEntries), entries)
			}
		})
	}
}

func TestServiceInstallFromTheCheckoutsDevBuild(t *testing.T) {
	// Without --source, a binary in the checkout's bin/ brings its tools.
	f := newFakeService(t, "linux")
	if err := os.WriteFile(f.src+"/bin/fridolin", []byte("dev build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.install(f.src+"/bin/fridolin", func(o *installOptions) { o.Source = "" }); err != nil {
		t.Fatalf("%v\n%s", err, f.out)
	}
	if m, _ := readInstallManifest(f.dir()); m.Source != f.src || !pathExists(f.dir()+"/tools/poll_results.py") {
		t.Errorf("install.json source %q, tools copied: %v", m.Source, pathExists(f.dir()+"/tools/poll_results.py"))
	}
}

// agentWithVariables is a launchd agent setup.sh wrote, with a login token
// and an API key added by hand.
const agentWithVariables = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
    "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>ai.fridolin.bridge</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/you/fridolin/fridolin</string>
        <string>--config</string>
        <string>/Users/you/.fridolin/config.json</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>HOME</key>
        <string>/Users/you/</string>
        <key>PATH</key>
        <string>/usr/bin:/bin</string>
        <key>CLAUDE_CODE_OAUTH_TOKEN</key>
        <string>secret &amp; value</string>
        <key>GROQ_API_KEY</key>
        <string>gsk-test</string>
    </dict>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
`

func TestServiceInstallKeepsVariablesAddedToTheAgent(t *testing.T) {
	// launchd has no drop-ins, so a login token added to the plist used to
	// be lost on every setup.sh run, and every brain failed "Not logged in".
	// An editor may put a value on lines of its own; launchd passes it on
	// as it is, and so does the install.
	const byHandValue = "\n            gsk-test\n        "
	byHand := strings.Replace(agentWithVariables, "<string>gsk-test</string>", "<string>"+byHandValue+"</string>", 1)
	cases := []struct {
		name   string
		agent  string
		binary bool // the agent was saved as a binary plist, as `defaults write` does
		dryRun bool
	}{
		{"XML agent", agentWithVariables, false, false},
		{"binary agent", agentWithVariables, true, false},
		{"agent edited by hand", byHand, false, false},
		// README offers the dry run to look at the unit: it must not
		// copy a token into a terminal or an assistant's transcript.
		{"dry run", agentWithVariables, false, true},
		{"dry run of an agent edited by hand", byHand, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeService(t, "darwin")
			unitPath, _ := f.h.unitFile()
			makeFiles(t, filepath.Dir(unitPath), "./")
			if err := os.WriteFile(unitPath, []byte(c.agent), 0o644); err != nil {
				t.Fatal(err)
			}
			if c.binary {
				plutil, err := exec.LookPath("plutil")
				if err != nil {
					t.Skip("plutil is macOS-only")
				}
				if out, err := exec.Command(plutil, "-convert", "binary1", unitPath).CombinedOutput(); err != nil {
					t.Fatalf("plutil: %v: %s", err, out)
				}
			}
			before := readFile(t, unitPath)
			f.loaded = true
			if err := f.install(f.exe("build one"), func(o *installOptions) { o.DryRun = c.dryRun }); err != nil {
				t.Fatalf("%v\n%s", err, f.out)
			}
			out := f.out.String()
			if !strings.Contains(out, "CLAUDE_CODE_OAUTH_TOKEN, GROQ_API_KEY") || strings.Contains(out, "secret") || strings.Contains(out, "gsk-test") {
				t.Errorf("output must name the kept keys, never their values:\n%s", out)
			}
			if c.dryRun {
				if readFile(t, unitPath) != before || len(f.cmds) != 0 {
					t.Errorf("the dry run changed the agent or ran %q", f.cmds)
				}
				if !strings.Contains(out, "<key>GROQ_API_KEY</key>") || !strings.Contains(out, keptValueShown) {
					t.Errorf("the dry run does not show the kept keys with a stand-in value:\n%s", out)
				}
				return
			}
			u, err := readUnit("darwin", []byte(readFile(t, unitPath)))
			if err != nil {
				t.Fatal(err)
			}
			path, _ := servicePath(f.h.path, "", f.home, "darwin", nil)
			want := map[string]string{"HOME": f.home, "PATH": path, "CLAUDE_CODE_OAUTH_TOKEN": "secret & value", "GROQ_API_KEY": "gsk-test"}
			if c.agent == byHand {
				want["GROQ_API_KEY"] = byHandValue
			}
			for k, v := range want {
				if u.Env[k] != v {
					t.Errorf("%s = %q, want %q", k, u.Env[k], v)
				}
			}
			// The old agent ran the checkout's binary; it is unloaded first.
			if !slices.Contains(f.cmds, "launchctl bootout gui/501/"+serviceLabel) {
				t.Errorf("ran %q, want a bootout of the running agent", f.cmds)
			}
		})
	}
}

func TestServiceInstallLeavesAnAgentItCannotReadAlone(t *testing.T) {
	// Replacing it would drop every variable added to it without a word.
	cases := []struct{ name, content string }{
		{"a broken binary plist", "bplist00 this is no property list"},
		{"a cut-off XML plist", agentWithVariables[:len(agentWithVariables)/2]},
		{"a plist without a dict", `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<plist version="1.0"><string>x</string></plist>` + "\n"},
		// plutil reads a bare word as an old-style plist holding a string.
		{"a stray word", "garbage"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeService(t, "darwin")
			unitPath, _ := f.h.unitFile()
			makeFiles(t, filepath.Dir(unitPath), "./")
			if err := os.WriteFile(unitPath, []byte(c.content), 0o600); err != nil {
				t.Fatal(err)
			}
			err := f.install(f.exe("build"))
			var unchanged *unchangedError
			if !errors.As(err, &unchanged) || !strings.Contains(err.Error(), unitPath) || !strings.Contains(err.Error(), "would be lost") {
				t.Fatalf("err = %v, want a refusal that names %s", err, unitPath)
			}
			if readFile(t, unitPath) != c.content || pathExists(f.dir()) {
				t.Errorf("the agent was replaced (%v) or the binary installed (%v)", readFile(t, unitPath) != c.content, pathExists(f.dir()))
			}
			for _, cmd := range f.cmds {
				if strings.HasPrefix(cmd, "launchctl") {
					t.Errorf("ran %q", cmd)
				}
			}
		})
	}
}

func TestServiceInstallKeepsToolsWithTheirBinary(t *testing.T) {
	// A bare binary has no tools/ next to it. The tools of each build stay
	// with it, so a rollback pairs the two again; a build that came
	// without tools goes back without them.
	cases := []struct {
		name      string
		withTools []bool // the installs in order: with the checkout's tools, or bare
		wantBack  string // the build a rollback goes back to
		wantTools bool   // and whether it has tools
	}{
		{"tools, then tools", []bool{true, true}, "build 1", true},
		{"tools, then bare", []bool{true, false}, "build 1", true},
		{"tools, then bare twice", []bool{true, false, false}, "build 2", false},
		{"tools, bare, then tools", []bool{true, false, true}, "build 2", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeService(t, "linux")
			dir, bin := f.dir(), f.dir()+"/fridolin"
			for i, tools := range c.withTools {
				if err := f.install(f.exe(fmt.Sprintf("build %d", i+1)), func(o *installOptions) {
					if !tools {
						o.Source = ""
					}
				}); err != nil {
					t.Fatalf("install %d: %v\n%s", i+1, err, f.out)
				}
				if got := pathExists(dir + "/tools/poll_results.py"); got != tools {
					t.Errorf("after install %d: tools %v, want %v", i+1, got, tools)
				}
			}
			if err := f.h.rollback(f.cfg, dir); err != nil {
				t.Fatalf("%v\n%s", err, f.out)
			}
			if got, tools := readFile(t, bin), pathExists(dir+"/tools/poll_results.py"); got != c.wantBack || tools != c.wantTools {
				t.Errorf("after the rollback: fridolin %q with tools %v, want %q with tools %v", got, tools, c.wantBack, c.wantTools)
			}
		})
	}
}

func TestServiceInstallRefusesThePreviousBinary(t *testing.T) {
	f := newFakeService(t, "linux")
	bin := f.dir() + "/fridolin"
	for _, build := range []string{"build one", "build two"} {
		if err := f.install(f.exe(build)); err != nil {
			t.Fatalf("%v\n%s", err, f.out)
		}
	}
	// fridolin.prev would bring the tools of the install it replaces.
	err := f.install(bin + ".prev")
	var unchanged *unchangedError
	if !errors.As(err, &unchanged) || !strings.Contains(err.Error(), "service rollback") {
		t.Fatalf("installing fridolin.prev: %v, want a refusal that points at rollback", err)
	}
	if readFile(t, bin) != "build two" || readFile(t, bin+".prev") != "build one" {
		t.Errorf("the refused install swapped the binaries")
	}
}

func TestInstallDirForTheRunningBinary(t *testing.T) {
	// setup.sh prints `$INSTALL_DIR/fridolin service rollback`, which must
	// act on $INSTALL_DIR even when that is not the default.
	f := newFakeService(t, "linux")
	custom := f.home + "/opt/fridolin"
	for _, build := range []string{"build one", "build two"} {
		if err := f.install(f.exe(build), func(o *installOptions) { o.Dir = custom }); err != nil {
			t.Fatalf("%v\n%s", err, f.out)
		}
	}
	link := f.home + "/bin/fridolin"
	makeFiles(t, f.home, "bin/")
	if err := os.Symlink(custom+"/fridolin", link); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, exe, want string }{
		{"the installed binary", custom + "/fridolin", custom},
		{"the one it replaced", custom + "/fridolin.prev", custom},
		{"a symlink to it", link, custom},
		{"a build elsewhere", f.exe("build"), defaultInstallDir(f.home)},
		{"a build in the checkout", f.src + "/bin/fridolin", defaultInstallDir(f.home)},
		{"unknown", "", defaultInstallDir(f.home)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := installDirFor(c.exe, f.home); got != c.want {
				t.Errorf("installDirFor(%s) = %s, want %s", c.exe, got, c.want)
			}
		})
	}
	if err := f.h.rollback(f.cfg, installDirFor(custom+"/fridolin", f.home)); err != nil {
		t.Fatalf("rollback: %v\n%s", err, f.out)
	}
	if readFile(t, custom+"/fridolin") != "build one" {
		t.Errorf("the rollback did not act on %s", custom)
	}
}

func TestServiceInstallRetiresTheAgentFromBeforeTheRename(t *testing.T) {
	f := newFakeService(t, "darwin")
	legacy := f.home + "/Library/LaunchAgents/" + legacyServiceLabel + ".plist"
	makeFiles(t, "/", strings.TrimPrefix(legacy, "/"))
	if err := f.install(f.exe("build one")); err != nil {
		t.Fatalf("%v\n%s", err, f.out)
	}
	if pathExists(legacy) || !pathExists(legacy+".replaced-by-fridolin") {
		t.Errorf("the legacy plist was not moved aside")
	}
	if !slices.Contains(f.cmds, "launchctl bootout gui/501/"+legacyServiceLabel) {
		t.Errorf("ran %q, want the legacy agent booted out", f.cmds)
	}
}

func TestServiceInstallRefusesUnsafeFolders(t *testing.T) {
	f := newFakeService(t, "darwin")
	makeFiles(t, f.home, "shared/notes.txt")
	cases := []struct{ name, dir, want string }{
		{"relative", "lib/fridolin", "not an absolute path"},
		{"the home folder", f.home, "cannot be"},
		{"the root", "/", "cannot be"},
		{"inside the data folder", f.home + "/.fridolin/bin", "inside the data folder"},
		{"inside the checkout", f.src + "/bin", "inside the source checkout"},
		{"a folder with other files", f.home + "/shared", "holds notes.txt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := f.install(f.exe("build"), func(o *installOptions) { o.Dir = c.dir })
			var unchanged *unchangedError
			if !errors.As(err, &unchanged) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q, saying nothing changed", err, c.want)
			}
			if len(f.cmds) != 0 {
				t.Errorf("ran %q", f.cmds)
			}
			unitPath, _ := f.h.unitFile()
			if pathExists(unitPath) {
				t.Errorf("wrote %s", unitPath)
			}
		})
	}
	if err := f.install(f.exe("build"), func(o *installOptions) { o.Source = f.home + "/shared" }); err == nil || !strings.Contains(err.Error(), "not a source checkout") {
		t.Errorf("--source of a folder that is no checkout: %v", err)
	}
	// Finder leaves a .DS_Store in a folder it opened.
	makeFiles(t, f.dir(), ".DS_Store")
	if err := f.install(f.exe("build")); err != nil {
		t.Errorf("install folder with a .DS_Store: %v", err)
	}
}

func TestServiceInstallDryRunChangesNothing(t *testing.T) {
	f := newFakeService(t, "linux")
	if err := f.install(f.exe("build"), func(o *installOptions) { o.DryRun = true }); err != nil {
		t.Fatal(err)
	}
	unitPath, _ := f.h.unitFile()
	if pathExists(f.dir()) || pathExists(unitPath) || len(f.cmds) != 0 {
		t.Errorf("dry run changed something: dir %v, unit %v, ran %q", pathExists(f.dir()), pathExists(unitPath), f.cmds)
	}
	if !strings.Contains(f.out.String(), "ExecStart=") {
		t.Errorf("dry run does not show the unit:\n%s", f.out)
	}
}

func TestServiceInstallNoStartLeavesTheServiceAlone(t *testing.T) {
	f := newFakeService(t, "darwin")
	if err := f.install(f.exe("build"), func(o *installOptions) { o.NoStart = true }); err != nil {
		t.Fatal(err)
	}
	unitPath, _ := f.h.unitFile()
	if !pathExists(unitPath) || !pathExists(f.dir()+"/fridolin") || len(f.cmds) != 0 {
		t.Errorf("unit %v, binary %v, ran %q", pathExists(unitPath), pathExists(f.dir()+"/fridolin"), f.cmds)
	}
	if !strings.Contains(f.out.String(), "keeps its old binary") {
		t.Errorf("output:\n%s", f.out)
	}
}

func TestServiceInstallChecksTheNewBridgeAnswers(t *testing.T) {
	// The health check used to pass against the old process, so setup
	// reported success while the old binary kept running.
	ready := func(v httpapi.ReadyResponse) func() (int, []byte) {
		return func() (int, []byte) {
			data, _ := json.Marshal(v)
			return 200, data
		}
	}
	cases := []struct {
		name    string
		health  int
		ready   func() (int, []byte)
		listen  string
		wantErr string
		wantOut string
	}{
		{"new bridge, paired", 200, func() (int, []byte) {
			return ready(httpapi.ReadyResponse{Ready: true, WhatsApp: "connected", Version: version.String(), StartedAt: time.Now()})()
		}, "", "", "WhatsApp is connected"},
		{"dev mode", 200, func() (int, []byte) {
			return ready(httpapi.ReadyResponse{Ready: true, WhatsApp: "disabled", Version: version.String(), StartedAt: time.Now()})()
		}, "", "", "runs in dev mode"},
		{"logged out", 200, func() (int, []byte) {
			return ready(httpapi.ReadyResponse{WhatsApp: "logged-out", Version: version.String(), StartedAt: time.Now()})()
		}, "", "", "pair again"},
		{"the old bridge still answers", 200, ready(httpapi.ReadyResponse{WhatsApp: "connected", Version: version.String(), StartedAt: time.Now().Add(-time.Hour)}), "", "is still the one started", ""},
		{"the old bridge answers while it shuts down", 200, func() func() (int, []byte) {
			calls := 0
			return func() (int, []byte) {
				if calls++; calls < 3 {
					return ready(httpapi.ReadyResponse{WhatsApp: "connected", Version: "v0.0.1", StartedAt: time.Now().Add(-time.Hour)})()
				}
				return ready(httpapi.ReadyResponse{WhatsApp: "connected", Version: version.String(), StartedAt: time.Now()})()
			}
		}(), "", "", "the bridge is running"},
		{"another build answers", 200, func() (int, []byte) {
			return ready(httpapi.ReadyResponse{WhatsApp: "connected", Version: "v0.0.1", StartedAt: time.Now()})()
		}, "", "runs version v0.0.1", ""},
		{"a build without /ready", 200, func() (int, []byte) { return 404, []byte("404 page not found\n") }, "", "older build", ""},
		{"nothing answers", 0, nil, "", "did not answer", ""},
		{"http off", 0, nil, "off", "", "nothing to check"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeService(t, "darwin")
			f.health = c.health
			if c.ready != nil {
				f.ready = c.ready
			}
			if c.listen != "" {
				f.cfg = writeConfig(t, `{"brain_mode": true, "data_dir": "`+f.home+`/.fridolin", "http_listen": "`+c.listen+`"}`)
			}
			err := f.install(f.exe("build"))
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("%v\n%s", err, f.out)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("err = %v, want %q", err, c.wantErr)
			}
			if c.wantErr != "" && c.wantErr != "older build" && !strings.Contains(err.Error(), "tail -F ~/Library/Logs/fridolin/stderr.log") {
				t.Errorf("err = %v, want the log hint", err)
			}
			if !strings.Contains(f.out.String(), c.wantOut) {
				t.Errorf("output lacks %q:\n%s", c.wantOut, f.out)
			}
		})
	}
}

func TestServiceInstallGivesUpOnANewBridgeThatDoesNotComeUp(t *testing.T) {
	old := func(code int, v httpapi.ReadyResponse) func() (int, []byte) {
		return func() (int, []byte) {
			data, _ := json.Marshal(v)
			if code != 200 {
				data = []byte("404 page not found\n")
			}
			return code, data
		}
	}
	oldBridge := httpapi.ReadyResponse{WhatsApp: "connected", Version: "v0.0.1", StartedAt: time.Now().Add(-time.Hour)}
	cases := []struct {
		name    string
		ready   func() (int, []byte)
		answers int  // /health polls the old bridge answers before nothing does
		hang    bool // the port takes connections, and each poll times out
		wantErr string
	}{
		// The new bridge crashed at start: the error must not blame the old
		// one, which answered only while it shut down.
		{"the old bridge answers twice, then nothing", old(200, oldBridge), 2, false, "the new bridge did not come up"},
		// The first move from the checkout's binary: that build has no /ready.
		{"an old build without /ready answers, then nothing", old(404, oldBridge), 2, false, "the new bridge did not come up"},
		{"a port that takes connections but never answers", nil, 0, true, "did not answer on"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeService(t, "darwin")
			if c.ready != nil {
				f.ready = c.ready
			}
			start, get, polls := f.clock, f.h.get, 0
			f.h.get = func(url string) (int, []byte, error) {
				if strings.HasSuffix(url, "/health") {
					polls++
				}
				switch {
				case c.hang:
					f.clock = f.clock.Add(2 * time.Second) // the client's timeout
					return 0, nil, errors.New("timeout")
				case polls > c.answers:
					return 0, nil, errors.New("connection refused")
				}
				return get(url)
			}
			err := f.install(f.exe("build"))
			if err == nil || !strings.Contains(err.Error(), c.wantErr) || !strings.Contains(err.Error(), f.h.logHint()) {
				t.Fatalf("err = %v, want %q and the log hint", err, c.wantErr)
			}
			// It gives up at the deadline, however long each poll takes.
			if took := f.clock.Sub(start); took > f.h.wait+2*2*time.Second {
				t.Errorf("waited %s for a wait of %s", took, f.h.wait)
			}
		})
	}
}

func TestServiceRollbackSwapsInThePreviousInstall(t *testing.T) {
	f := newFakeService(t, "linux")
	if err := f.install(f.exe("build one")); err != nil {
		t.Fatal(err)
	}
	if err := f.h.rollback(f.cfg, f.dir()); err == nil || !strings.Contains(err.Error(), "no previous install") {
		t.Fatalf("rollback with nothing to go back to: %v", err)
	}
	if err := os.WriteFile(f.src+"/tools/poll_results.py", []byte("print('poll 2')\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.install(f.exe("build two")); err != nil {
		t.Fatal(err)
	}
	bin := f.dir() + "/fridolin"
	for i, want := range []string{"build one", "build two"} {
		f.cmds = nil
		if err := f.h.rollback(f.cfg, f.dir()); err != nil {
			t.Fatalf("rollback %d: %v\n%s", i+1, err, f.out)
		}
		other := map[string]string{"build one": "build two", "build two": "build one"}[want]
		if readFile(t, bin) != want || readFile(t, bin+".prev") != other {
			t.Errorf("rollback %d: fridolin %q, fridolin.prev %q", i+1, readFile(t, bin), readFile(t, bin+".prev"))
		}
		tool := map[string]string{"build one": "print('poll')\n", "build two": "print('poll 2')\n"}[want]
		if readFile(t, f.dir()+"/tools/poll_results.py") != tool {
			t.Errorf("rollback %d: tools not swapped", i+1)
		}
		if !slices.Contains(f.cmds, "systemctl --user restart fridolin.service") {
			t.Errorf("rollback %d ran %q, want a restart", i+1, f.cmds)
		}
	}
}

func TestServiceStatus(t *testing.T) {
	f := newFakeService(t, "darwin")
	if err := f.h.status(f.cfg, f.dir()); err != nil {
		t.Fatalf("status before any install: %v", err)
	}
	if out := f.out.String(); !strings.Contains(out, "installed: nothing") || !strings.Contains(out, "service:   not installed") {
		t.Errorf("output:\n%s", out)
	}
	if err := f.install(f.exe("build")); err != nil {
		t.Fatal(err)
	}
	f.out.Reset()
	if err := f.h.status(f.cfg, f.dir()); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	for _, want := range []string{"installed: " + f.dir() + "/fridolin (" + version.String(), "from " + f.src, "service:   running, pid 4242", "runs:      " + f.dir() + "/fridolin", "bridge:    answers on http://127.0.0.1:18765: " + version.String() + ", WhatsApp pairing"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "not the installed binary") {
		t.Errorf("warns about the installed binary:\n%s", out)
	}

	// An agent setup.sh wrote before runs the checkout's binary.
	unitPath, _ := f.h.unitFile()
	if err := os.WriteFile(unitPath, []byte(strings.ReplaceAll(readFile(t, unitPath), f.dir()+"/fridolin", f.src+"/fridolin")), 0o600); err != nil {
		t.Fatal(err)
	}
	f.health = 0
	f.out.Reset()
	if err := f.h.status(f.cfg, f.dir()); !errors.Is(err, errBridgeDown) {
		t.Errorf("status with no bridge answering: %v, want errBridgeDown", err)
	}
	if out := f.out.String(); !strings.Contains(out, "not the installed binary") || !strings.Contains(out, "does not answer") {
		t.Errorf("output:\n%s", out)
	}

	// setup.sh took a bridge that cannot be asked for one that runs.
	off := writeConfig(t, `{"brain_mode": true, "data_dir": "`+f.home+`/.fridolin", "http_listen": "off"}`)
	f.out.Reset()
	if err := f.h.status(off, f.dir()); !errors.Is(err, errCannotAsk) {
		t.Errorf("status with http_listen off: %v, want errCannotAsk", err)
	}
}

func TestServiceUninstall(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			f := newFakeService(t, goos)
			if err := f.install(f.exe("build")); err != nil {
				t.Fatal(err)
			}
			unitPath, _ := f.h.unitFile()
			f.cmds = nil
			if err := f.h.uninstall(f.dir(), false); err != nil {
				t.Fatal(err)
			}
			if pathExists(unitPath) || pathExists(f.dir()) {
				t.Errorf("unit %v, install folder %v left", pathExists(unitPath), pathExists(f.dir()))
			}
			if !pathExists(f.cfg) || !pathExists(f.home+"/.fridolin") || !pathExists(f.src+"/tools/poll_results.py") {
				t.Errorf("uninstall removed the config, the data folder or the checkout")
			}
			want := []string{"launchctl bootout gui/501/" + serviceLabel}
			if goos == "linux" {
				want = []string{"systemctl --user disable --now fridolin.service", "systemctl --user daemon-reload"}
			}
			if !slices.Equal(f.cmds, want) {
				t.Errorf("ran %q, want %q", f.cmds, want)
			}
		})
	}

	t.Run("a folder that is not an install", func(t *testing.T) {
		f := newFakeService(t, "linux")
		if err := f.install(f.exe("build")); err != nil {
			t.Fatal(err)
		}
		makeFiles(t, f.home, "shared/notes.txt")
		f.cmds = nil
		if err := f.h.uninstall(f.home+"/shared", false); err == nil {
			t.Errorf("uninstall emptied a folder with other files")
		}
		if !pathExists(f.home + "/shared/notes.txt") {
			t.Errorf("notes.txt removed")
		}
		// It refuses before it stops anything.
		if unitPath, _ := f.h.unitFile(); !pathExists(unitPath) || len(f.cmds) != 0 {
			t.Errorf("the refused uninstall removed the unit (%v) or ran %q", !pathExists(unitPath), f.cmds)
		}
	})
}

func TestServiceRefusesOtherSystems(t *testing.T) {
	f := newFakeService(t, "freebsd")
	if err := f.install(f.exe("build")); err == nil || !strings.Contains(err.Error(), "macOS (launchd) and Linux (systemd)") {
		t.Errorf("install on freebsd: %v", err)
	}
	if fmt.Sprint(f.cmds) != "[]" {
		t.Errorf("ran %q", f.cmds)
	}
}
