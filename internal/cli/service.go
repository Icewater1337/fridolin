package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/controlcli"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/version"
)

const (
	serviceLabel       = "ai.fridolin.bridge"
	legacyServiceLabel = "com.whatsapp-claude-bridge" // compat: the launchd label before the rename, see retireLegacyAgent
	systemdUnitName    = "fridolin.service"
)

// What the install folder holds. install and uninstall replace or remove
// these, so a folder with anything else in it is refused.
const (
	installedBinary    = "fridolin"
	prevSuffix         = ".prev"
	prevManifestFile   = "install.prev.json"
	installTempPrefix  = ".fridolin-"
	serviceWaitDefault = 30 * time.Second
)

var installEntries = []string{
	installedBinary, installedBinary + prevSuffix,
	"tools", "tools" + prevSuffix,
	platform.InstallManifestFile, prevManifestFile,
}

// defaultInstallDir is where `service install` puts the bridge.
func defaultInstallDir(home string) string {
	return filepath.Join(home, ".local", "lib", "fridolin")
}

// installDirFor is the install folder a service command acts on when no
// --dir is given: the one exe runs from when that is an install (it holds
// install.json), so `<dir>/fridolin service rollback` acts on <dir>
// whatever folder it was installed to, else the default.
func installDirFor(exe, home string) string {
	if exe != "" {
		if p, err := filepath.EvalSymlinks(exe); err == nil {
			exe = p
		}
		if dir := filepath.Dir(exe); pathExists(filepath.Join(dir, platform.InstallManifestFile)) {
			return dir
		}
	}
	return defaultInstallDir(home)
}

// serviceHost is what the service commands act on. Tests fill it with a
// temp HOME and a fake runner, so they never reach launchctl or systemctl.
type serviceHost struct {
	goos string
	home string
	uid  int
	path string // the installer's PATH; the service gets it too
	// brainPath is the installer's brainPATHVar, the folders a bridge
	// that installs itself took off its PATH; see servicePath.
	brainPath string
	out       io.Writer

	// run runs launchctl or systemctl and returns what it printed.
	run   func(name string, args ...string) (string, error)
	get   func(url string) (int, []byte, error)
	sleep func(time.Duration)
	now   func() time.Time // for the deadline of the wait below
	wait  time.Duration    // how long to wait for the bridge after a restart
}

func newServiceHost() *serviceHost {
	home := app.HomeDir()
	client := &http.Client{Timeout: 2 * time.Second}
	return &serviceHost{
		goos:      runtime.GOOS,
		home:      home,
		uid:       os.Getuid(),
		path:      os.Getenv("PATH"),
		brainPath: os.Getenv(app.BrainPATHVar),
		out:       os.Stdout,
		run: func(name string, args ...string) (string, error) {
			// systemctl restart waits for the old bridge to stop, which
			// takes seconds; a minute means something is stuck.
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			return string(out), err
		},
		get: func(url string) (int, []byte, error) {
			resp, err := client.Get(url)
			if err != nil {
				return 0, nil, err
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
			return resp.StatusCode, body, err
		},
		sleep: time.Sleep,
		now:   time.Now,
		wait:  serviceWaitDefault,
	}
}

func (h *serviceHost) printf(format string, a ...any) {
	fmt.Fprintf(h.out, "  "+format+"\n", a...)
}

const serviceUsage = `Usage:
  fridolin service install [--config PATH] [--dir DIR] [--source CHECKOUT] [--no-start] [--dry-run]
      copy this binary and the Python tools to DIR, keeping the previous ones for
      a rollback, write the launchd agent (macOS) or systemd user unit (Linux)
      that runs them, restart it and wait for the bridge; exits 5 when it
      refused before it changed anything
  fridolin service rollback [--config PATH] [--dir DIR]
      swap in the previous install and restart
  fridolin service restart [--config PATH]
  fridolin service status [--config PATH] [--dir DIR]
      exits 0 when the bridge answers, 3 when it does not, 4 when it cannot
      be asked (http_listen is off)
  fridolin service uninstall [--dir DIR] [--keep-files]
      stop the service and remove it and the install; your config, data and
      brains stay

DIR is the folder this binary runs from when that is an install, else
~/.local/lib/fridolin.
`

// Exit codes beyond 1, for setup.sh and other scripts.
const (
	exitBridgeDown = 3 // status: the bridge does not answer
	exitCannotAsk  = 4 // status: http_listen is off, so the bridge cannot be asked
	exitUnchanged  = 5 // install: refused before it changed anything
)

var (
	errCannotAsk = errors.New("http_listen is off, so the bridge cannot be asked")
)

// unchangedError is an install that stopped before it wrote anything, so
// there is nothing to roll back.
type unchangedError struct{ err error }

func (e *unchangedError) Error() string { return e.err.Error() }

func (e *unchangedError) Unwrap() error { return e.err }

func serviceCmd(args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(serviceUsage)
		return
	}
	h := newServiceHost()
	sub, args := args[0], args[1:]
	exe, exeErr := os.Executable()
	fs := flag.NewFlagSet("service "+sub, flag.ExitOnError)
	cfgFlag := fs.String("config", "", configFlagHelp)
	dirFlag := fs.String("dir", "", "install folder (default: this binary's when it is an install, else ~/.local/lib/fridolin)")
	cfgPath := func() string { return configPath(*cfgFlag) }
	dir := func() string {
		if *dirFlag != "" {
			return *dirFlag
		}
		return installDirFor(exe, h.home)
	}
	var err error
	switch sub {
	case "install":
		source := fs.String("source", "", "source checkout to take tools/ from and to record (default: the one this binary was built in)")
		noStart := fs.Bool("no-start", false, "write the files but leave the running service alone")
		dry := fs.Bool("dry-run", false, "print what would be done and the unit, change nothing")
		_ = fs.Parse(args)
		if exeErr != nil {
			err = &unchangedError{fmt.Errorf("finding this binary: %w", exeErr)}
			break
		}
		err = h.install(installOptions{ConfigPath: cfgPath(), Dir: dir(), Exe: exe, Source: *source, NoStart: *noStart, DryRun: *dry})
	case "rollback":
		_ = fs.Parse(args)
		err = h.rollback(cfgPath(), dir())
	case "restart":
		_ = fs.Parse(args)
		err = h.restartCmd(cfgPath())
	case "status":
		_ = fs.Parse(args)
		err = h.status(cfgPath(), dir())
	case "uninstall":
		keep := fs.Bool("keep-files", false, "remove the service but keep the installed files")
		_ = fs.Parse(args)
		err = h.uninstall(dir(), *keep)
	default:
		fmt.Fprintf(os.Stderr, "unknown service command %q\n\n%s", sub, serviceUsage)
		os.Exit(2)
	}
	var unchanged *unchangedError
	switch {
	case errors.Is(err, controlcli.ErrBridgeDown):
		os.Exit(exitBridgeDown)
	case errors.Is(err, errCannotAsk):
		os.Exit(exitCannotAsk)
	case errors.As(err, &unchanged):
		fmt.Fprintf(os.Stderr, "fridolin service %s: %v (nothing was changed)\n", sub, err)
		os.Exit(exitUnchanged)
	case err != nil:
		fmt.Fprintf(os.Stderr, "fridolin service %s: %v\n", sub, err)
		os.Exit(1)
	}
}

// unitFile is the launchd agent or systemd unit this host uses.
func (h *serviceHost) unitFile() (string, error) {
	switch h.goos {
	case "darwin":
		return filepath.Join(h.home, "Library", "LaunchAgents", serviceLabel+".plist"), nil
	case "linux":
		return filepath.Join(h.home, ".config", "systemd", "user", systemdUnitName), nil
	}
	return "", fmt.Errorf("background services are supported on macOS (launchd) and Linux (systemd), not %s; run the bridge by hand", h.goos)
}

func (h *serviceHost) logDir() string {
	return filepath.Join(h.home, "Library", "Logs", "fridolin")
}

// logHint says where to look when the bridge does not come up.
func (h *serviceHost) logHint() string {
	if h.goos == "darwin" {
		return "tail -F ~/Library/Logs/fridolin/stderr.log"
	}
	return "journalctl --user -u fridolin -f"
}

func (h *serviceHost) launchdTarget() string {
	return fmt.Sprintf("gui/%d/%s", h.uid, serviceLabel)
}

// servicePath is the PATH the service runs with: the installer's own, so
// brains find what you find in your shell (claude, uv, node, ffmpeg), then
// the usual folders it may lack. launchd and systemd start with a minimal
// PATH of their own. Relative entries and missing folders are left out,
// and so are the folders inside the brain folders owners name, from which
// the bridge runs nothing: they go into brainPath, the service's
// brainPATHVar, with those of the installer's own brainPATHVar (a bridge
// that installs itself), and the bridge hands each to the runs of the
// brain whose folder holds it (see setBridgePATH).
func servicePath(installer, installerBrain, home, goos string, owners []app.PathOwner) (path, brainPath string) {
	var out, brain []string
	add := func(dir string, mustExist, inBrain bool) {
		dir = filepath.Clean(dir)
		if !filepath.IsAbs(dir) || (mustExist && !platform.IsDir(dir)) {
			return
		}
		list := &out
		if _, in := app.PathOwnerOf(dir, owners); in || inBrain {
			list = &brain
		}
		if !slices.Contains(*list, dir) {
			*list = append(*list, dir)
		}
	}
	for _, dir := range filepath.SplitList(installerBrain) {
		if dir != "" {
			add(dir, true, true)
		}
	}
	for _, dir := range filepath.SplitList(installer) {
		if dir != "" {
			add(dir, true, false)
		}
	}
	defaults := []string{filepath.Join(home, ".local", "bin"), "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}
	if goos == "darwin" {
		defaults = append([]string{"/opt/homebrew/bin"}, defaults...)
	}
	for _, dir := range defaults {
		add(dir, false, false)
	}
	sep := string(filepath.ListSeparator)
	return strings.Join(out, sep), strings.Join(brain, sep)
}

type envVar struct{ Key, Value string }

// unitData fills the templates below. Every value is a full path or
// variable as the service must see it; the templates escape them.
type unitData struct {
	Label   string
	Binary  string
	Config  string
	WorkDir string
	LogDir  string
	Env     []envVar // HOME and PATH first, then brainPATHVar if set; launchd only gets more
}

const launchdTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
    "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!--
  launchd agent for Fridolin, written by "fridolin service install", which
  rewrites it on every install. Variables you add under EnvironmentVariables
  are kept. "fridolin service uninstall" removes it.
-->
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{xml .Label}}</string>

    <key>ProgramArguments</key>
    <array>
        <string>{{xml .Binary}}</string>
        <string>--config</string>
        <string>{{xml .Config}}</string>
        <string>--log-file</string>
        <string>{{xml .LogDir}}/stderr.log</string>
    </array>

    <key>WorkingDirectory</key>
    <string>{{xml .WorkDir}}</string>

    <key>EnvironmentVariables</key>
    <dict>
{{- range .Env}}
        <key>{{xml .Key}}</key>
        <string>{{xml .Value}}</string>
{{- end}}
    </dict>

    <key>StandardOutPath</key>
    <string>{{xml .LogDir}}/stdout.log</string>

    <key>StandardErrorPath</key>
    <string>{{xml .LogDir}}/stderr.log</string>

    <key>RunAtLoad</key>
    <true/>

    <key>KeepAlive</key>
    <true/>

    <key>ThrottleInterval</key>
    <integer>10</integer>
</dict>
</plist>
`

const systemdTemplate = `# systemd user unit for Fridolin, written by "fridolin service install",
# which rewrites it on every install. Keep your own settings in a drop-in:
#     systemctl --user edit fridolin
# "fridolin service uninstall" removes it. To keep the bridge running after
# you log out: sudo loginctl enable-linger $USER
#
# The sandbox layer is macOS-only: here brains run with whatever access this
# user has, so run the bridge as a dedicated user.

[Unit]
Description=Fridolin

[Service]
Type=simple
WorkingDirectory={{path .WorkDir}}
ExecStart={{exec .Binary}} --config {{exec .Config}}
{{- range .Env}}
Environment={{quote (printf "%s=%s" .Key .Value)}}
{{- end}}
Restart=on-failure
RestartSec=5s
# Nothing the bridge or a brain starts can gain privileges (no sudo).
NoNewPrivileges=yes

[Install]
WantedBy=default.target
`

var unitFuncs = template.FuncMap{
	"xml": func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	},
	// systemd expands %-specifiers in all three settings, and $VAR in
	// ExecStart; inside double quotes it reads C escapes.
	"path":  func(s string) string { return strings.ReplaceAll(s, "%", "%%") },
	"quote": func(s string) string { return systemdQuote(s, false) },
	"exec":  func(s string) string { return systemdQuote(s, true) },
}

func systemdQuote(s string, exec bool) string {
	r := []string{`\`, `\\`, `"`, `\"`, "%", "%%"}
	if exec {
		r = append(r, "$", "$$")
	}
	return `"` + strings.NewReplacer(r...).Replace(s) + `"`
}

// renderUnit writes the unit for goos, refusing values the format cannot
// hold: control characters, and for systemd a backslash, which its
// WorkingDirectory cannot escape. A plist holds a tab or a line break in
// a variable, escaped: one written by hand often has its value on lines
// of its own.
func renderUnit(goos string, d unitData) ([]byte, error) {
	control := func(r rune) bool { return r < 0x20 || r == 0x7f }
	for _, v := range []string{d.Label, d.Binary, d.Config, d.WorkDir, d.LogDir} {
		if strings.ContainsFunc(v, control) {
			return nil, fmt.Errorf("%q has a control character, which a service file cannot hold", v)
		}
	}
	inValue := control
	if goos != "linux" {
		inValue = func(r rune) bool { return control(r) && r != '\t' && r != '\n' && r != '\r' }
	}
	for _, e := range d.Env {
		if strings.ContainsFunc(e.Key, control) {
			return nil, fmt.Errorf("the variable name %q has a control character, which a service file cannot hold", e.Key)
		}
		// Named, never shown: the variables kept from the agent may be
		// credentials.
		if strings.ContainsFunc(e.Value, inValue) {
			return nil, fmt.Errorf("the value of %s has a control character, which a service file cannot hold", e.Key)
		}
	}
	src := launchdTemplate
	if goos == "linux" {
		src = systemdTemplate
		if strings.Contains(d.WorkDir, `\`) || strings.TrimSpace(d.WorkDir) != d.WorkDir {
			return nil, fmt.Errorf("the data folder %q has a backslash or surrounding spaces, which a systemd unit cannot hold", d.WorkDir)
		}
	}
	tpl, err := template.New("unit").Funcs(unitFuncs).Parse(src)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := tpl.Execute(&b, d); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// parsePlist reads an XML property list into maps, slices, strings and
// bools: enough for the launchd agents this file writes, and the ones
// setup.sh wrote before it.
func parsePlist(data []byte) (any, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	se, end, err := nextStart(d)
	if err != nil || end || se.Name.Local != "plist" {
		return nil, fmt.Errorf("not a property list: %v", err)
	}
	se, end, err = nextStart(d)
	if err != nil || end {
		return nil, fmt.Errorf("empty property list: %v", err)
	}
	return plistValue(d, se)
}

// nextStart skips to the next element, or reports the end of the current one.
func nextStart(d *xml.Decoder) (xml.StartElement, bool, error) {
	for {
		tok, err := d.Token()
		if err != nil {
			return xml.StartElement{}, false, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return t, false, nil
		case xml.EndElement:
			return xml.StartElement{}, true, nil
		}
	}
}

func plistValue(d *xml.Decoder, se xml.StartElement) (any, error) {
	switch se.Name.Local {
	case "dict":
		m := map[string]any{}
		for {
			k, end, err := nextStart(d)
			if err != nil {
				return nil, err
			}
			if end {
				return m, nil
			}
			key, err := plistValue(d, k)
			if err != nil {
				return nil, err
			}
			v, end, err := nextStart(d)
			if err != nil || end {
				return nil, fmt.Errorf("dict key %v has no value", key)
			}
			val, err := plistValue(d, v)
			if err != nil {
				return nil, err
			}
			ks, _ := key.(string)
			m[ks] = val
		}
	case "array":
		var a []any
		for {
			v, end, err := nextStart(d)
			if err != nil {
				return nil, err
			}
			if end {
				return a, nil
			}
			val, err := plistValue(d, v)
			if err != nil {
				return nil, err
			}
			a = append(a, val)
		}
	case "true", "false":
		return se.Name.Local == "true", d.Skip()
	default: // key, string, integer, real, date, data
		var s string
		err := d.DecodeElement(&s, &se)
		return s, err
	}
}

// installedUnit is what the unit file on disk says: the binary it runs and,
// for launchd, its environment.
type installedUnit struct {
	Binary string
	Env    map[string]string
}

func readUnit(goos string, data []byte) (installedUnit, error) {
	var u installedUnit
	if goos == "linux" {
		for _, line := range strings.Split(string(data), "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart="); ok {
				u.Binary = firstSystemdWord(rest)
			}
		}
		return u, nil
	}
	v, err := parsePlist(data)
	if err != nil {
		return u, err
	}
	// plutil reads a stray word as an old-style plist holding a string.
	root, ok := v.(map[string]any)
	if !ok {
		return u, errors.New("not a launchd agent: the property list holds no dict")
	}
	if args, ok := root["ProgramArguments"].([]any); ok && len(args) > 0 {
		u.Binary, _ = args[0].(string)
	}
	if env, ok := root["EnvironmentVariables"].(map[string]any); ok {
		u.Env = map[string]string{}
		for k, v := range env {
			if s, ok := v.(string); ok {
				u.Env[k] = s
			}
		}
	}
	return u, nil
}

// readUnitFile reads the unit at p, whose content is data. On macOS a
// plist that is not XML (`defaults write` and some editors save the agent
// as a binary plist) is converted with plutil first.
func (h *serviceHost) readUnitFile(p string, data []byte) (installedUnit, error) {
	u, err := readUnit(h.goos, data)
	if err == nil || h.goos != "darwin" {
		return u, err
	}
	out, cerr := h.run("plutil", "-convert", "xml1", "-o", "-", p)
	if cerr != nil {
		return u, fmt.Errorf("%v, and plutil cannot convert it: %v: %s", err, cerr, strings.TrimSpace(out))
	}
	return readUnit(h.goos, []byte(out))
}

// firstSystemdWord undoes what the exec template function does.
func firstSystemdWord(s string) string {
	s = strings.TrimSpace(s)
	var word string
	if strings.HasPrefix(s, `"`) {
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
				continue
			}
			if s[i] == '"' {
				break
			}
			b.WriteByte(s[i])
		}
		word = b.String()
	} else {
		word, _, _ = strings.Cut(s, " ")
	}
	return strings.NewReplacer("%%", "%", "$$", "$").Replace(word)
}

type installOptions struct {
	ConfigPath string
	Dir        string // the install folder
	Exe        string // the binary to install: the one running
	Source     string // checkout to take tools/ from; "" for the binary's own
	NoStart    bool
	DryRun     bool
}

// checkInstallDir refuses an install folder that the bridge cannot run
// from safely, or that holds anything but an earlier install.
func checkInstallDir(dir, home, dataDir string, l platform.BridgeLayout) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("the install folder %q is not an absolute path", dir)
	}
	cdir := fsx.Canonical(dir)
	switch {
	case cdir == "/" || cdir == fsx.Canonical(home):
		return fmt.Errorf("the install folder cannot be %s; use a folder of its own such as %s", dir, defaultInstallDir(home))
	case dataDir != "" && fsx.Within(cdir, fsx.Canonical(dataDir)):
		return fmt.Errorf("the install folder cannot be inside the data folder %s: the sandbox keeps brains from reading the tools there", dataDir)
	case l.Source != "" && fsx.Within(cdir, fsx.Canonical(l.Source)):
		return fmt.Errorf("the install folder cannot be inside the source checkout %s: a build there would reach the service again", l.Source)
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		// Finder leaves a .DS_Store in every folder it shows.
		if e.Name() == ".DS_Store" {
			continue
		}
		if !slices.Contains(installEntries, e.Name()) && !strings.HasPrefix(e.Name(), installTempPrefix) {
			return fmt.Errorf("%s holds %s, which an install does not: pick an empty folder, or the default %s", dir, e.Name(), defaultInstallDir(home))
		}
	}
	return nil
}

// installPlan is what an install writes, worked out before it writes
// anything.
type installPlan struct {
	cfg      *config.File
	unitPath string
	dir      string
	binary   string                // the installed binary
	from     platform.BridgeLayout // where the tools come from, and the checkout to record
	data     unitData
	kept     []string // the variables kept from the agent, by name
	old      []byte   // the unit file as it is, or nil
	unit     []byte
}

// keptValueShown stands in for the values a dry run does not print: the
// variables kept from the agent may be credentials.
const keptValueShown = "(kept, not shown)"

func (h *serviceHost) install(o installOptions) error {
	p, err := h.planInstall(o)
	if err != nil {
		return &unchangedError{err}
	}

	if o.DryRun {
		h.printf("would install %s to %s", o.Exe, p.binary)
		if p.from.Tools != "" {
			h.printf("would copy the tools from %s to %s", p.from.Tools, filepath.Join(p.dir, "tools"))
		}
		if len(p.kept) > 0 {
			h.printf("would keep the variables you added to it: %s (their values are not shown)", strings.Join(p.kept, ", "))
		}
		shown := p.data
		shown.Env = slices.Clone(p.data.Env)
		for i, e := range shown.Env {
			if slices.Contains(p.kept, e.Key) {
				shown.Env[i].Value = keptValueShown
			}
		}
		unit, err := renderUnit(h.goos, shown)
		if err != nil {
			return err
		}
		h.printf("would write %s:", p.unitPath)
		fmt.Fprintf(h.out, "\n%s\n", unit)
		return nil
	}

	dir, binary := p.dir, p.binary
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	changed, err := installFile(o.Exe, binary)
	if err != nil {
		return fmt.Errorf("installing the binary: %w", err)
	}
	switch {
	case !changed:
		h.printf("%s is already this build (%s)", binary, version.String())
	case pathExists(binary + prevSuffix):
		h.printf("installed %s (%s); the one it replaced is %s", binary, version.String(), installedBinary+prevSuffix)
	default:
		h.printf("installed %s (%s)", binary, version.String())
	}
	// The tools that came with the binary it replaced stay with it in
	// tools.prev; the same binary again only refreshes them.
	if err := h.installTools(p.from.Tools, dir, changed); err != nil {
		return err
	}
	if changed || !pathExists(filepath.Join(dir, platform.InstallManifestFile)) {
		if err := writeManifest(dir, p.from.Source, changed); err != nil {
			return err
		}
	}

	// The service starts in the data folder, which launchd and systemd
	// refuse when it is not there; the bridge makes the rest as it starts.
	if err := os.MkdirAll(p.data.WorkDir, 0o700); err != nil {
		return err
	}
	if h.goos == "darwin" {
		if err := os.MkdirAll(p.data.LogDir, 0o700); err != nil {
			return err
		}
		// The logs name chats and senders.
		if err := os.Chmod(p.data.LogDir, 0o700); err != nil {
			return err
		}
	}
	if p.old == nil || !bytes.Equal(p.old, p.unit) {
		// 0600: the variables kept in a plist may be credentials.
		if err := fsx.WriteFileAtomic(p.unitPath, p.unit, 0o600, false); err != nil {
			return fmt.Errorf("writing %s: %w", p.unitPath, err)
		}
		h.printf("wrote %s", p.unitPath)
		if len(p.kept) > 0 {
			h.printf("kept the variables you added to it: %s", strings.Join(p.kept, ", "))
		}
	}
	if o.NoStart {
		h.printf("not restarted (--no-start): a bridge that is running keeps its old binary until it restarts")
		return nil
	}
	if h.goos == "darwin" {
		h.retireLegacyAgent()
	}
	return h.restart(p.cfg, p.unitPath, version.String())
}

// planInstall checks everything an install needs and renders the unit,
// writing nothing.
func (h *serviceHost) planInstall(o installOptions) (*installPlan, error) {
	unitPath, err := h.unitFile()
	if err != nil {
		return nil, err
	}
	cfgPath, err := filepath.Abs(o.ConfigPath)
	if err != nil {
		return nil, err
	}
	cfg, err := app.LoadConfig(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", cfgPath, err)
	}
	dataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	p := &installPlan{cfg: cfg, unitPath: unitPath, dir: filepath.Clean(o.Dir)}

	// Where the tools come from, and the checkout to record: the one
	// named, else the binary's own (a build in the checkout, or a release
	// archive with tools/ next to the binary).
	p.from = platform.LayoutOf(o.Exe)
	if o.Source != "" {
		src, err := filepath.Abs(o.Source)
		if err != nil {
			return nil, err
		}
		if !platform.IsCheckout(src) {
			return nil, fmt.Errorf("%s is not a source checkout (no go.mod and tools/)", src)
		}
		p.from = platform.BridgeLayout{Dir: src, Tools: filepath.Join(src, "tools"), Source: src}
	}
	if err := checkInstallDir(p.dir, h.home, dataDir, p.from); err != nil {
		return nil, err
	}
	p.binary = filepath.Join(p.dir, installedBinary)
	// fridolin.prev installed over fridolin would keep the tools of the
	// build it replaces, and a rollback would pair the wrong two.
	if fsx.Canonical(filepath.Dir(o.Exe)) == fsx.Canonical(p.dir) && !sameFile(o.Exe, p.binary) {
		return nil, fmt.Errorf("%s is part of the install in %s; to go back to it, run: %s service rollback", o.Exe, p.dir, p.binary)
	}

	p.data = unitData{
		Label:   serviceLabel,
		Binary:  p.binary,
		Config:  cfgPath,
		WorkDir: dataDir,
		LogDir:  h.logDir(),
	}
	path, brainPath := servicePath(h.path, h.brainPath, h.home, h.goos, app.PathOwners(cfg))
	p.data.Env = []envVar{{"HOME", h.home}, {"PATH", path}}
	if brainPath != "" {
		p.data.Env = append(p.data.Env, envVar{app.BrainPATHVar, brainPath})
	}
	old, err := os.ReadFile(unitPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", unitPath, err)
	default:
		p.old = old
	}
	if h.goos == "darwin" && p.old != nil {
		// launchd has no drop-ins: what you added to the agent's
		// environment (a login token, an API key) stays. An agent that
		// cannot be read is left alone rather than replaced without them.
		u, err := h.readUnitFile(unitPath, p.old)
		if err != nil {
			return nil, fmt.Errorf("%s cannot be read (%v), so the variables added to it would be lost: fix it, or move it aside to install a new one", unitPath, err)
		}
		for k := range u.Env {
			// Install writes these itself, brainPATHVar only while a
			// folder of PATH lies in a brain folder.
			if k != "HOME" && k != "PATH" && k != app.BrainPATHVar {
				p.kept = append(p.kept, k)
			}
		}
		sort.Strings(p.kept)
		for _, k := range p.kept {
			p.data.Env = append(p.data.Env, envVar{k, u.Env[k]})
		}
	}
	if p.unit, err = renderUnit(h.goos, p.data); err != nil {
		return nil, err
	}
	return p, nil
}

// installFile copies src to dst, keeping the file it replaces as
// dst.prev. dst exists at every moment, so launchd or systemd starting
// the bridge meanwhile finds one or the other. It reports false when dst
// was already src or a copy of it.
func installFile(src, dst string) (bool, error) {
	if sameFile(src, dst) {
		return false, nil
	}
	tmp, err := copyToTemp(src, filepath.Dir(dst), 0o755)
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp)
	if same, err := sameContent(tmp, dst); err == nil && same {
		return false, nil
	}
	if pathExists(dst) {
		if err := keepAs(dst, dst+prevSuffix); err != nil {
			return false, err
		}
	}
	return true, os.Rename(tmp, dst)
}

// keepAs puts a copy of file p at dst, leaving p in place: a hard link
// where the file system has them, else a copy.
func keepAs(p, dst string) error {
	link := filepath.Join(filepath.Dir(p), installTempPrefix+"keep")
	_ = os.Remove(link)
	if err := os.Link(p, link); err != nil {
		tmp, cerr := copyToTemp(p, filepath.Dir(p), 0o755)
		if cerr != nil {
			return fmt.Errorf("keeping %s: %w", p, cerr)
		}
		link = tmp
	}
	if err := os.Rename(link, dst); err != nil {
		os.Remove(link)
		return err
	}
	return nil
}

func copyToTemp(src, dir string, perm os.FileMode) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, installTempPrefix+"*")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Chmod(perm)
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

func sameFile(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	return err == nil && os.SameFile(sa, sb)
}

func sameContent(a, b string) (bool, error) {
	ha, err := fileHash(a)
	if err != nil {
		return false, err
	}
	hb, err := fileHash(b)
	return err == nil && ha == hb, err
}

func fileHash(p string) ([32]byte, error) {
	var sum [32]byte
	f, err := os.Open(p)
	if err != nil {
		return sum, err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return sum, err
	}
	copy(sum[:], hash.Sum(nil))
	return sum, nil
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// installTools replaces dir/tools with the helpers (*.py) and their
// requirements files (*.txt) from src. With keepPrev the old copy becomes
// tools.prev, for a rollback.
func (h *serviceHost) installTools(src, dir string, keepPrev bool) error {
	dst := filepath.Join(dir, "tools")
	if keepPrev && !platform.IsDir(dst) {
		// The binary just replaced came without tools: a tools.prev left
		// from an install before it would go back with the wrong build.
		if err := os.RemoveAll(dst + prevSuffix); err != nil {
			return err
		}
	}
	if src == "" {
		h.printf("! no tools/ next to this binary: brains will not find the Python helpers (pass --source with the checkout)")
		if keepPrev && platform.IsDir(dst) {
			// They came with the binary just replaced, and a rollback
			// takes them back with it.
			if err := os.RemoveAll(dst + prevSuffix); err != nil {
				return err
			}
			if err := os.Rename(dst, dst+prevSuffix); err != nil {
				return err
			}
			h.printf("the tools of the build it replaced are in tools%s, for a rollback", prevSuffix)
		}
		return nil
	}
	if sameFile(src, dst) {
		return nil
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("reading the tools: %w", err)
	}
	tmp, err := os.MkdirTemp(dir, installTempPrefix+"tools-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	n := 0
	for _, e := range entries {
		name := e.Name()
		// Regular files only: a symlink would point the installed copy
		// back into the checkout.
		if !e.Type().IsRegular() || !(strings.HasSuffix(name, ".py") || strings.HasSuffix(name, ".txt")) {
			continue
		}
		perm := os.FileMode(0o644)
		if info, err := e.Info(); err == nil && info.Mode()&0o100 != 0 {
			perm = 0o755
		}
		f, err := copyToTemp(filepath.Join(src, name), tmp, perm)
		if err != nil {
			return err
		}
		if err := os.Rename(f, filepath.Join(tmp, name)); err != nil {
			return err
		}
		n++
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if platform.IsDir(dst) {
		old := dst + prevSuffix
		if !keepPrev {
			old = filepath.Join(dir, installTempPrefix+"old-tools")
		}
		if err := os.RemoveAll(old); err != nil {
			return err
		}
		if err := os.Rename(dst, old); err != nil {
			return err
		}
		if !keepPrev {
			defer os.RemoveAll(old)
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	h.printf("copied %d tools from %s", n, src)
	return nil
}

// writeManifest records the install. With keepPrev the record it replaces
// is kept for a rollback.
func writeManifest(dir, source string, keepPrev bool) error {
	p := filepath.Join(dir, platform.InstallManifestFile)
	if data, err := os.ReadFile(p); err == nil && keepPrev {
		if err := fsx.WriteFileAtomic(filepath.Join(dir, prevManifestFile), data, 0o644, false); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(platform.InstallManifest{Version: version.String(), Source: source, InstalledAt: time.Now().UTC().Truncate(time.Second)}, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(p, append(data, '\n'), 0o644, false)
}

// retireLegacyAgent stops the agent setup.sh installed before the rename,
// which would run a second bridge on the same data.
func (h *serviceHost) retireLegacyAgent() {
	old := filepath.Join(h.home, "Library", "LaunchAgents", legacyServiceLabel+".plist")
	if !pathExists(old) {
		return
	}
	_, _ = h.run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", h.uid, legacyServiceLabel))
	if err := os.Rename(old, old+".replaced-by-fridolin"); err == nil {
		h.printf("retired the old agent %s", old)
	}
}

// restart (re)starts the service so it runs what is installed now, then
// waits for the bridge to answer; want is the version that should, or ""
// for whichever is installed. A loaded agent is unloaded and loaded again:
// launchd reads a rewritten plist only then, and bootout stops the bridge
// with SIGTERM, so it shuts down cleanly. systemd's `enable --now` would
// leave a running unit alone, hence the restart.
func (h *serviceHost) restart(cfg *config.File, unitPath, want string) error {
	started := time.Now()
	switch h.goos {
	case "darwin":
		if _, err := h.run("launchctl", "print", h.launchdTarget()); err == nil {
			_, _ = h.run("launchctl", "bootout", h.launchdTarget())
		}
		// bootout can return before the old bridge has exited, and
		// bootstrap fails until it has.
		domain := fmt.Sprintf("gui/%d", h.uid)
		for i := 0; ; i++ {
			out, err := h.run("launchctl", "bootstrap", domain, unitPath)
			if err == nil {
				break
			}
			if i == 19 {
				return fmt.Errorf("launchctl bootstrap %s %s: %v: %s", domain, unitPath, err, strings.TrimSpace(out))
			}
			h.sleep(time.Second)
		}
	case "linux":
		for _, args := range [][]string{{"daemon-reload"}, {"enable", systemdUnitName}, {"restart", systemdUnitName}} {
			if out, err := h.run("systemctl", append([]string{"--user"}, args...)...); err != nil {
				return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(out))
			}
		}
	}
	h.printf("restarted the service")
	return h.waitForBridge(cfg, started, want)
}

// bridgeAnswer is what one poll of the bridge got.
type bridgeAnswer struct {
	up      bool // /health answered
	r       httpapi.ReadyResponse
	readErr error // /ready gave no httpapi.ReadyResponse: an older build
}

func (h *serviceHost) askBridge(base string) bridgeAnswer {
	var a bridgeAnswer
	if code, _, err := h.get(base + "/health"); err != nil || code != http.StatusOK {
		return a
	}
	a.up = true
	_, body, err := h.get(base + "/ready")
	if err == nil {
		err = json.Unmarshal(body, &a.r)
	}
	a.readErr = err
	return a
}

// waitForBridge waits until the bridge answers /health and /ready says it
// started after since: the old one may still answer for a moment while it
// shuts down. Then it checks the version is want (unless that is "") and
// says whether WhatsApp still needs pairing.
func (h *serviceHost) waitForBridge(cfg *config.File, since time.Time, want string) error {
	if !cfg.HTTPEnabled() {
		h.printf("http_listen is off, so there is nothing to check; see the log: %s", h.logHint())
		return nil
	}
	base := "http://" + cfg.HTTPListen
	const every = 500 * time.Millisecond
	// A deadline, not a number of polls: a port that takes connections but
	// never answers makes each poll last the client's timeout, twice.
	deadline := h.now().Add(h.wait)
	var (
		answered bool         // some poll got an answer, from the old bridge maybe
		last     bridgeAnswer // what the last poll got
	)
	for {
		last = h.askBridge(base)
		answered = answered || last.up
		if last.up && last.readErr == nil && !last.r.StartedAt.Before(since) {
			break
		}
		if !h.now().Before(deadline) {
			break
		}
		h.sleep(every)
	}
	r := last.r
	switch {
	case !last.up && answered:
		return fmt.Errorf("the new bridge did not come up: the old one answered on %s while it shut down, then nothing answered within %s; see the log: %s", base, h.wait, h.logHint())
	case !last.up:
		return fmt.Errorf("the bridge did not answer on %s/health within %s; see the log: %s", base, h.wait, h.logHint())
	case last.readErr != nil:
		return fmt.Errorf("the bridge on %s answers /health but not /ready, so it is an older build: %v", base, last.readErr)
	case r.StartedAt.Before(since):
		return fmt.Errorf("the bridge on %s is still the one started at %s (version %s): another bridge holds the port, or the service did not restart; see the log: %s", base, r.StartedAt.Local().Format(time.DateTime), r.Version, h.logHint())
	case want != "" && r.Version != want:
		return fmt.Errorf("the bridge on %s runs version %s, not %s that was installed; see the log: %s", base, r.Version, want, h.logHint())
	}
	h.printf("the bridge is running (%s)", r.Version)
	switch r.WhatsApp {
	case "connected":
		h.printf("WhatsApp is connected")
	case "disabled":
		h.printf("WhatsApp is off: the bridge runs in dev mode (--no-whatsapp)")
	case "logged-out", "replaced":
		h.printf("! WhatsApp is %s: pair again in the dashboard (fridolin open)", r.WhatsApp)
	default:
		h.printf("WhatsApp is not connected yet (%s): pair it in the dashboard (fridolin open) if you have not", r.WhatsApp)
	}
	return nil
}

func (h *serviceHost) restartCmd(cfgPath string) error {
	unitPath, err := h.unitFile()
	if err != nil {
		return err
	}
	if !pathExists(unitPath) {
		return fmt.Errorf("no service installed (%s is missing); run: fridolin service install", unitPath)
	}
	cfg, err := app.LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("loading %s: %w", cfgPath, err)
	}
	return h.restart(cfg, unitPath, "")
}

// rollback swaps the install and the one it replaced (the binary, the
// tools and the record of both), then restarts. Running it again swaps
// them back.
func (h *serviceHost) rollback(cfgPath, dir string) error {
	unitPath, err := h.unitFile()
	if err != nil {
		return err
	}
	binary := filepath.Join(dir, installedBinary)
	if !pathExists(binary + prevSuffix) {
		return fmt.Errorf("there is no previous install to go back to (%s is missing)", binary+prevSuffix)
	}
	cfg, err := app.LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("loading %s: %w", cfgPath, err)
	}
	if err := checkInstallDir(dir, h.home, cfg.DataDir, platform.BridgeLayout{}); err != nil {
		return err
	}
	// The binary is never missing: the current one is kept aside first,
	// then the previous one renamed over it.
	swap := filepath.Join(dir, installTempPrefix+"swap")
	if err := keepAs(binary, swap); err != nil {
		return err
	}
	if err := os.Rename(binary+prevSuffix, binary); err != nil {
		os.Remove(swap)
		return err
	}
	if err := os.Rename(swap, binary+prevSuffix); err != nil {
		return err
	}
	for _, pair := range [][2]string{{"tools", "tools" + prevSuffix}, {platform.InstallManifestFile, prevManifestFile}} {
		if err := swapPaths(filepath.Join(dir, pair[0]), filepath.Join(dir, pair[1])); err != nil {
			return err
		}
	}
	want, shown := "", "unknown version"
	if m, err := platform.ReadInstallManifest(dir); err == nil {
		want, shown = m.Version, m.Version
	}
	h.printf("swapped in the previous install (%s); `fridolin service rollback` again undoes it", shown)
	if !pathExists(unitPath) {
		return nil
	}
	return h.restart(cfg, unitPath, want)
}

// swapPaths exchanges a and b, either of which may be missing.
func swapPaths(a, b string) error {
	tmp := filepath.Join(filepath.Dir(a), installTempPrefix+"swap-"+filepath.Base(a))
	_ = os.RemoveAll(tmp)
	aOK, bOK := pathExists(a), pathExists(b)
	if aOK {
		if err := os.Rename(a, tmp); err != nil {
			return err
		}
	}
	if bOK {
		if err := os.Rename(b, a); err != nil {
			return err
		}
	}
	if aOK {
		return os.Rename(tmp, b)
	}
	return nil
}

func (h *serviceHost) status(cfgPath, dir string) error {
	unitPath, err := h.unitFile()
	if err != nil {
		return err
	}
	binary := filepath.Join(dir, installedBinary)
	if m, err := platform.ReadInstallManifest(dir); err == nil && pathExists(binary) {
		from := ""
		if m.Source != "" {
			from = ", from " + m.Source
		}
		h.printf("installed: %s (%s, %s%s)", binary, m.Version, m.InstalledAt.Local().Format(time.DateTime), from)
		if pathExists(binary + prevSuffix) {
			prev := "unknown version"
			if data, err := os.ReadFile(filepath.Join(dir, prevManifestFile)); err == nil {
				var pm platform.InstallManifest
				if json.Unmarshal(data, &pm) == nil {
					prev = pm.Version
				}
			}
			h.printf("rollback:  %s (%s)", binary+prevSuffix, prev)
		}
	} else {
		h.printf("installed: nothing in %s", dir)
	}

	data, err := os.ReadFile(unitPath)
	switch {
	case err != nil:
		h.printf("service:   not installed (%s)", unitPath)
	default:
		state := h.serviceState()
		h.printf("service:   %s (%s)", state, unitPath)
		if u, err := h.readUnitFile(unitPath, data); err == nil && u.Binary != "" {
			h.printf("runs:      %s", u.Binary)
			if !sameFile(u.Binary, binary) {
				h.printf("! that is not the installed binary: run fridolin service install")
			}
		}
	}

	cfg, err := app.LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("loading %s: %w", cfgPath, err)
	}
	if !cfg.HTTPEnabled() {
		// Not "running": setup.sh must not tell a bridge that cannot be
		// asked from one that answers.
		h.printf("bridge:    http_listen is off, so it cannot be asked")
		return errCannotAsk
	}
	base := "http://" + cfg.HTTPListen
	a := h.askBridge(base)
	switch {
	case !a.up:
		h.printf("bridge:    does not answer on %s", base)
		return controlcli.ErrBridgeDown
	case a.readErr != nil || a.r.Version == "":
		h.printf("bridge:    answers on %s (a build without /ready)", base)
		return nil
	}
	h.printf("bridge:    answers on %s: %s, WhatsApp %s, up since %s", base, a.r.Version, a.r.WhatsApp, a.r.StartedAt.Local().Format(time.DateTime))
	return nil
}

// serviceState asks launchd or systemd how the service is doing.
func (h *serviceHost) serviceState() string {
	if h.goos == "linux" {
		out, _ := h.run("systemctl", "--user", "is-active", systemdUnitName)
		return strings.TrimSpace(out)
	}
	out, err := h.run("launchctl", "print", h.launchdTarget())
	if err != nil {
		return "not loaded"
	}
	state, pid := "loaded", ""
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " = ")
		switch {
		case !ok:
		case k == "state":
			state = v
		case k == "pid":
			if _, err := strconv.Atoi(v); err == nil {
				pid = v
			}
		}
	}
	if pid != "" {
		return state + ", pid " + pid
	}
	return state
}

func (h *serviceHost) uninstall(dir string, keepFiles bool) error {
	unitPath, err := h.unitFile()
	if err != nil {
		return err
	}
	removeFiles := !keepFiles && platform.IsDir(dir)
	// Before the service stops: a folder that is no install refuses the
	// whole uninstall, not only the part after it.
	if removeFiles {
		if err := checkInstallDir(dir, h.home, "", platform.BridgeLayout{}); err != nil {
			return err
		}
	}
	if pathExists(unitPath) {
		if h.goos == "darwin" {
			_, _ = h.run("launchctl", "bootout", h.launchdTarget())
		} else {
			_, _ = h.run("systemctl", "--user", "disable", "--now", systemdUnitName)
		}
		if err := os.Remove(unitPath); err != nil {
			return err
		}
		if h.goos == "linux" {
			_, _ = h.run("systemctl", "--user", "daemon-reload")
		}
		h.printf("stopped the service and removed %s", unitPath)
	} else {
		h.printf("no service installed (%s)", unitPath)
	}
	if !removeFiles {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	if err := os.Remove(dir); err != nil {
		return err
	}
	h.printf("removed %s; your configuration, data folder and brains are untouched", dir)
	return nil
}
