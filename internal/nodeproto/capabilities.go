package nodeproto

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Capabilities is what a node declares it can do, and, in State, what the
// core approved of it. A family that is nil or false is not offered.
type Capabilities struct {
	Exec   *ExecCap `json:"exec,omitempty"`
	FS     *FSCap   `json:"fs,omitempty"`
	Host   bool     `json:"host,omitempty"`
	Notify bool     `json:"notify,omitempty"`
	// Fride is FrIDE on the node, declared while it answers.
	Fride *FrideCap `json:"fride,omitempty"`
	// AndroidBuild (android-build) is Gradle with a JDK and the Android
	// SDK on the node, declared while the agent finds them.
	AndroidBuild *AndroidBuildCap `json:"android_build,omitempty"`
}

// ExecCap runs allowlisted commands.
type ExecCap struct {
	Allow []ExecRule `json:"allow"`
	// TimeoutSeconds stops a command after this long (0:
	// DefaultExecTimeoutSeconds).
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// MaxOutputBytes caps stdout and stderr each (0: DefaultMaxOutputBytes).
	MaxOutputBytes int `json:"max_output_bytes,omitempty"`
}

// ExecRule allows one program.
type ExecRule struct {
	// Name is argv[0] exactly as the call gives it: a bare program name,
	// which the node looks up in its PATH, or an absolute path.
	Name string `json:"name"`
	// Args, when not null, are regular expressions; each argument of the
	// call must match one of them whole. null allows any arguments, [] none.
	Args []string `json:"args"`
}

// FSCap reads, and where granted writes, files under its roots.
type FSCap struct {
	Roots []FSRoot `json:"roots"`
}

// FSRoot is one granted folder.
type FSRoot struct {
	// Path is absolute on the node.
	Path string `json:"path"`
	// Write grants fs.write under it; without it the root is read-only.
	Write bool `json:"write,omitempty"`
}

// Defaults and bounds of the exec family.
const (
	DefaultExecTimeoutSeconds = 60
	MaxExecTimeoutSeconds     = 3600
	DefaultMaxOutputBytes     = 1 << 20
	MaxOutputBytesLimit       = 16 << 20
	maxRules                  = 64
	maxPatterns               = 32
	maxPatternLen             = 256
	maxRoots                  = 32
	maxPathLen                = 4096
)

// Timeout is the exec timeout in seconds, the default filled in.
func (e *ExecCap) Timeout() int {
	if e == nil || e.TimeoutSeconds == 0 {
		return DefaultExecTimeoutSeconds
	}
	return e.TimeoutSeconds
}

// OutputCap is the output cap in bytes, the default filled in.
func (e *ExecCap) OutputCap() int {
	if e == nil || e.MaxOutputBytes == 0 {
		return DefaultMaxOutputBytes
	}
	return e.MaxOutputBytes
}

// shells are the programs no exec rule may name: shells and what runs a
// command line or another program for the caller, which would turn an
// allowlist of one into an allowlist of everything. Compared by base name,
// lower case, without .exe.
var shells = []string{
	"sh", "bash", "zsh", "fish", "dash", "ksh", "mksh", "csh", "tcsh", "ash", "busybox", "nu", "xonsh", "elvish",
	"cmd", "command", "powershell", "pwsh", "powershell_ise", "wsl", "wslhost",
	"env", "sudo", "su", "doas", "osascript", "cscript", "wscript", "mshta", "xargs", "nohup", "script",
}

// IsShell reports whether name, a program name or path in any OS's
// spelling, is a shell or a command runner an exec rule may not name.
func IsShell(name string) bool {
	base := name
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.ToLower(base)
	base = strings.TrimSuffix(base, ".exe")
	return slices.Contains(shells, base)
}

// DefaultExecAllow is the allowlist a node declares when its owner names
// none: programs that only report on the host, per OS. None is a shell.
func DefaultExecAllow(goos string) []ExecRule {
	none := []string{}
	switch goos {
	case "windows":
		return []ExecRule{{Name: "hostname", Args: none}, {Name: "whoami", Args: none}, {Name: "systeminfo", Args: none}}
	case "darwin":
		return []ExecRule{{Name: "uname", Args: []string{"-[amnrsv]+"}}, {Name: "sw_vers", Args: none}, {Name: "uptime", Args: none}, {Name: "df", Args: []string{"-[hkP]+"}}}
	default:
		return []ExecRule{{Name: "uname", Args: []string{"-[amnrsvpio]+"}}, {Name: "uptime", Args: none}, {Name: "df", Args: []string{"-[hkPT]+"}}}
	}
}

// isAbs reports whether p is absolute in Unix or Windows spelling: the
// core checks declarations of nodes on any OS.
func isAbs(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 3 && (p[0]|0x20) >= 'a' && (p[0]|0x20) <= 'z' && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// Check reports what is wrong with the declaration, nil when nothing is.
func (c Capabilities) Check() error {
	if e := c.Exec; e != nil {
		if len(e.Allow) > maxRules {
			return fmt.Errorf("exec: at most %d rules", maxRules)
		}
		if e.TimeoutSeconds < 0 || e.TimeoutSeconds > MaxExecTimeoutSeconds {
			return fmt.Errorf("exec: timeout_seconds from 0 to %d", MaxExecTimeoutSeconds)
		}
		if e.MaxOutputBytes < 0 || e.MaxOutputBytes > MaxOutputBytesLimit {
			return fmt.Errorf("exec: max_output_bytes from 0 to %d", MaxOutputBytesLimit)
		}
		for _, r := range e.Allow {
			if err := r.Check(); err != nil {
				return err
			}
		}
	}
	if f := c.FS; f != nil {
		if len(f.Roots) > maxRoots {
			return fmt.Errorf("fs: at most %d roots", maxRoots)
		}
		for _, r := range f.Roots {
			if r.Path == "" || len(r.Path) > maxPathLen || strings.ContainsRune(r.Path, 0) || !isAbs(r.Path) {
				return fmt.Errorf("fs: root %q must be an absolute path", r.Path)
			}
		}
	}
	if err := c.AndroidBuild.Check(); err != nil {
		return err
	}
	return c.Fride.Check()
}

// Check reports what is wrong with the rule.
func (r ExecRule) Check() error {
	switch {
	case r.Name == "" || len(r.Name) > maxPathLen || strings.ContainsRune(r.Name, 0):
		return fmt.Errorf("exec: rule name %q", r.Name)
	case strings.ContainsAny(r.Name, `/\`) && !isAbs(r.Name):
		return fmt.Errorf("exec: %q must be a bare program name or an absolute path", r.Name)
	case IsShell(r.Name):
		return fmt.Errorf("exec: %q is a shell or runs other programs; a node never allows one", r.Name)
	case len(r.Args) > maxPatterns:
		return fmt.Errorf("exec: %s: at most %d argument patterns", r.Name, maxPatterns)
	}
	for _, p := range r.Args {
		if len(p) > maxPatternLen {
			return fmt.Errorf("exec: %s: pattern longer than %d", r.Name, maxPatternLen)
		}
		if _, err := regexp.Compile(anchor(p)); err != nil {
			return fmt.Errorf("exec: %s: pattern %q: %v", r.Name, p, err)
		}
	}
	return nil
}

func anchor(p string) string { return `^(?:` + p + `)$` }

// Matches reports whether the rule allows argv.
func (r ExecRule) Matches(argv []string) bool {
	if len(argv) == 0 || argv[0] != r.Name {
		return false
	}
	if r.Args == nil {
		return true
	}
	res := make([]*regexp.Regexp, 0, len(r.Args))
	for _, p := range r.Args {
		re, err := regexp.Compile(anchor(p))
		if err != nil {
			return false
		}
		res = append(res, re)
	}
	for _, a := range argv[1:] {
		if !slices.ContainsFunc(res, func(re *regexp.Regexp) bool { return re.MatchString(a) }) {
			return false
		}
	}
	return true
}

func (r ExecRule) equal(o ExecRule) bool {
	return r.Name == o.Name && (r.Args == nil) == (o.Args == nil) && slices.Equal(r.Args, o.Args)
}

// ErrNotAllowed is an exec call no rule allows.
var ErrNotAllowed = errors.New("no exec rule allows this command")

// PermitsExec reports whether argv is allowed: by a rule, and never a
// shell, whatever a rule says.
func (c Capabilities) PermitsExec(argv []string) error {
	if c.Exec == nil {
		return errors.New("exec is not approved on this node")
	}
	if len(argv) == 0 || argv[0] == "" {
		return errors.New("exec: argv is empty")
	}
	if IsShell(argv[0]) {
		return fmt.Errorf("%w: %s is a shell", ErrNotAllowed, argv[0])
	}
	for _, r := range c.Exec.Allow {
		if r.Matches(argv) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrNotAllowed, argv[0])
}

// Allows reports whether the capabilities grant method at all; exec
// arguments and paths are checked by PermitsExec and on the node.
func (c Capabilities) Allows(method string) bool {
	switch method {
	case MethodExec:
		return c.Exec != nil && len(c.Exec.Allow) > 0
	case MethodFSList, MethodFSRead:
		return c.FS != nil && len(c.FS.Roots) > 0
	case MethodFSWrite:
		return c.FS != nil && slices.ContainsFunc(c.FS.Roots, func(r FSRoot) bool { return r.Write })
	case MethodHostInfo:
		return c.Host
	case MethodNotify:
		return c.Notify
	}
	if slices.Contains(FrideMethods, method) {
		return c.Fride != nil
	}
	if slices.Contains(AndroidMethods, method) {
		return c.AndroidBuild != nil
	}
	return false
}

// Empty reports whether nothing is offered.
func (c Capabilities) Empty() bool {
	return (c.Exec == nil || len(c.Exec.Allow) == 0) && (c.FS == nil || len(c.FS.Roots) == 0) && !c.Host && !c.Notify && c.Fride == nil && c.AndroidBuild == nil
}

// Covers reports whether approved covers every part of declared: each
// exec rule as it is, each root with no more access, limits no higher, and
// host and notify. A declaration that grows past what was approved needs
// approval again. FrIDE and android-build are covered by having been
// approved at all: FrIDE's address and repositories, and the JDK and
// platforms, are only what the node reports.
func Covers(approved, declared Capabilities) bool {
	if declared.Host && !approved.Host || declared.Notify && !approved.Notify || declared.Fride != nil && approved.Fride == nil {
		return false
	}
	if declared.AndroidBuild != nil && approved.AndroidBuild == nil {
		return false
	}
	if d := declared.Exec; d != nil && len(d.Allow) > 0 {
		a := approved.Exec
		if a == nil || d.Timeout() > a.Timeout() || d.OutputCap() > a.OutputCap() {
			return false
		}
		for _, r := range d.Allow {
			if !slices.ContainsFunc(a.Allow, r.equal) {
				return false
			}
		}
	}
	if d := declared.FS; d != nil {
		for _, r := range d.Roots {
			if approved.FS == nil || !slices.ContainsFunc(approved.FS.Roots, func(a FSRoot) bool { return a.Path == r.Path && (a.Write || !r.Write) }) {
				return false
			}
		}
	}
	return true
}

// Intersect is the part of declared that approved covers: what a node
// whose declaration grew may still serve while the rest waits.
func Intersect(declared, approved Capabilities) Capabilities {
	var out Capabilities
	out.Host = declared.Host && approved.Host
	out.Notify = declared.Notify && approved.Notify
	if declared.Fride != nil && approved.Fride != nil {
		f := *declared.Fride
		f.Repos = slices.Clone(f.Repos)
		out.Fride = &f
	}
	if declared.AndroidBuild != nil && approved.AndroidBuild != nil {
		ab := *declared.AndroidBuild
		ab.Platforms = slices.Clone(ab.Platforms)
		out.AndroidBuild = &ab
	}
	if d, a := declared.Exec, approved.Exec; d != nil && a != nil {
		e := &ExecCap{TimeoutSeconds: min(d.Timeout(), a.Timeout()), MaxOutputBytes: min(d.OutputCap(), a.OutputCap())}
		for _, r := range d.Allow {
			if slices.ContainsFunc(a.Allow, r.equal) {
				e.Allow = append(e.Allow, r)
			}
		}
		if len(e.Allow) > 0 {
			out.Exec = e
		}
	}
	if d, a := declared.FS, approved.FS; d != nil && a != nil {
		f := &FSCap{}
		for _, r := range d.Roots {
			i := slices.IndexFunc(a.Roots, func(x FSRoot) bool { return x.Path == r.Path })
			if i >= 0 {
				f.Roots = append(f.Roots, FSRoot{Path: r.Path, Write: r.Write && a.Roots[i].Write})
			}
		}
		if len(f.Roots) > 0 {
			out.FS = f
		}
	}
	return out
}

// Summary describes the capabilities in a few words, for the CLI, the
// dashboard and !fleet: "exec (3 commands), fs (1 root, 1 writable), host,
// fride".
func (c Capabilities) Summary() string {
	var parts []string
	if c.Exec != nil && len(c.Exec.Allow) > 0 {
		names := make([]string, len(c.Exec.Allow))
		for i, r := range c.Exec.Allow {
			names[i] = r.Name
		}
		parts = append(parts, "exec ("+strings.Join(names, ", ")+")")
	}
	if c.FS != nil && len(c.FS.Roots) > 0 {
		w := 0
		for _, r := range c.FS.Roots {
			if r.Write {
				w++
			}
		}
		s := fmt.Sprintf("fs (%d root", len(c.FS.Roots))
		if len(c.FS.Roots) != 1 {
			s += "s"
		}
		if w > 0 {
			s += fmt.Sprintf(", %d writable", w)
		}
		parts = append(parts, s+")")
	}
	if c.Host {
		parts = append(parts, "host")
	}
	if c.Notify {
		parts = append(parts, "notify")
	}
	if c.Fride != nil {
		parts = append(parts, "fride")
	}
	if c.AndroidBuild != nil {
		parts = append(parts, "android-build")
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, ", ")
}
