package nodeproto

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// SandboxCap is Docker Sandboxes (sbx) on the node: brains' runs in microVMs.
type SandboxCap struct {
	Version   string   `json:"version"` // sbx version
	SignedIn  bool     `json:"signed_in"`
	Daemon    bool     `json:"daemon"`              // sandboxd answers
	Settings  bool     `json:"settings"`            // fridolin's global settings hold
	Template  string   `json:"template,omitempty"`  // the template name fridolin made, "" before setup
	Providers []string `json:"providers,omitempty"` // "claude", "codex": those with a usable login
	Problem   string   `json:"problem,omitempty"`   // why it is not ready, plain
	// Bin is the version of the linux fridolin the node mounts into each
	// sandbox, "" before the core sent one (sandbox.bin). The core sends
	// its own when they differ, and runs no brain there until they match.
	Bin string `json:"bin,omitempty"`
}

// The providers a sandbox runs.
const (
	SandboxProviderClaude = "claude"
	SandboxProviderCodex  = "codex"
)

// The sandbox methods. Each needs the sandbox capability.
const (
	// MethodSandboxInfo reports sbx's state on the node (nothing →
	// SandboxInfo).
	MethodSandboxInfo = "sandbox.info"
	// MethodSandboxEnsure creates the brain's sandbox, or recreates it
	// when its mounts, memory, template or egress changed
	// (SandboxEnsureParams → SandboxEnsureResult).
	MethodSandboxEnsure = "sandbox.ensure"
	// MethodSandboxRun starts one run in the brain's sandbox
	// (SandboxRunParams → SandboxRunResult).
	MethodSandboxRun = "sandbox.run"
	// MethodSandboxRead reads a run's stdout from an offset, waiting up to
	// WaitMS for some (SandboxReadParams → SandboxReadResult).
	MethodSandboxRead = "sandbox.read"
	// MethodSandboxStop ends a run inside the VM: TERM to its process
	// group, KILL after the grace (SandboxStopParams → nothing).
	MethodSandboxStop = "sandbox.stop"
	// MethodSandboxRemove removes the brain's sandbox (SandboxRemoveParams
	// → nothing).
	MethodSandboxRemove = "sandbox.remove"
	// MethodSandboxBinWrite appends a chunk of the gzip'd linux fridolin
	// the core sends for the sandboxes, as update.write does
	// (FrideChunkParams → FrideTransfer).
	MethodSandboxBinWrite = "sandbox.bin_write"
	// MethodSandboxBin puts the sent binary in place of the one the
	// sandboxes mount, once its signature, platform and SHA-256 check
	// out (UpdateApplyParams with OS "linux" → nothing). The node declares
	// its version as SandboxCap.Bin.
	MethodSandboxBin = "sandbox.bin"
)

// SandboxMethods lists the sandbox methods.
var SandboxMethods = []string{
	MethodSandboxInfo, MethodSandboxEnsure, MethodSandboxRun, MethodSandboxRead, MethodSandboxStop, MethodSandboxRemove,
	MethodSandboxBinWrite, MethodSandboxBin,
}

// Bounds of the sandbox methods.
const (
	// MaxSandboxStdin bounds a run's stdin, the prompt, in bytes.
	MaxSandboxStdin = 4 << 20
	// MaxSandboxReadWaitMS bounds how long a sandbox.read waits.
	MaxSandboxReadWaitMS = 20000
	// SandboxRunBuffer is the most stdout the node keeps per run; a run
	// that writes more ends with an error.
	SandboxRunBuffer = 16 << 20
	// MaxSandboxRead is the most one sandbox.read returns, in bytes.
	MaxSandboxRead = 4 << 20
	// MaxSandboxEgress bounds a brain's extra egress hosts.
	MaxSandboxEgress = 50
	// MaxSandboxMounts bounds a sandbox's mounts.
	MaxSandboxMounts = 16
	// MaxSandboxArgs bounds a run's argv.
	MaxSandboxArgs = 256
	// MaxSandboxEnv bounds a run's environment, in variables.
	MaxSandboxEnv = 128
	// MaxSandboxArgBytes bounds argv and the environment together, bytes.
	MaxSandboxArgBytes = 512 << 10
	// MaxSandboxTokenLen bounds a run token.
	MaxSandboxTokenLen = 512
	// MinSandboxMemoryMB and MaxSandboxMemoryMB bound a sandbox's memory
	// (0: the node's default, DefaultSandboxMemoryMB).
	MinSandboxMemoryMB = 512
	MaxSandboxMemoryMB = 64 << 10
	// DefaultSandboxMemoryMB is a sandbox's memory when ensure names none.
	DefaultSandboxMemoryMB = 4 << 10
	// MaxSandboxTimeoutS bounds a run's timeout (0: none on the node; the
	// core keeps its own deadline).
	MaxSandboxTimeoutS = 24 * 3600
	// MaxSandboxGraceS bounds the grace of sandbox.stop (0:
	// DefaultSandboxGraceS).
	MaxSandboxGraceS = 120
	// DefaultSandboxGraceS is the grace of a stop that names none.
	DefaultSandboxGraceS = 5
	// MaxSandboxMarkerLen bounds the runtime marker.
	MaxSandboxMarkerLen = 128
	// MaxSandboxStderrTail is the most of stderr a read reports, bytes.
	MaxSandboxStderrTail = 8 << 10
	// MaxSandboxProblemLen bounds SandboxCap.Problem, in bytes.
	MaxSandboxProblemLen = 512
)

// SandboxRunForget is how long the node keeps a run that ended after
// its last read.
const SandboxRunForget = 10 * time.Minute

// SandboxMarkerFile is the file in a brain's folder that holds the
// core's runtime id: a node runs a brain only in the folder that holds
// the id the core sends (sandbox.ensure).
const SandboxMarkerFile = ".fridolin-runtime-id"

// SandboxName is the name of a brain's sandbox.
func SandboxName(brain string) string { return "fridolin-" + brain }

// SandboxTokenFile is the run's token file inside the VM, which the node
// names in FRIDOLIN_BRAIN_TOKEN_FILE. It holds a placeholder that sbx's
// proxy swaps for the run token on the way to the core's API.
func SandboxTokenFile(run string) string { return "/tmp/fridolin-run-" + run + ".token" }

// SandboxPidFile is the run's process group id inside the VM, which
// sandbox.stop signals.
func SandboxPidFile(run string) string { return "/tmp/fridolin-run-" + run + ".pid" }

var (
	sandboxBrainRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	sandboxRunRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	sandboxEnvRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	// sandboxHostRe is a host in sbx's pattern syntax: a host name with
	// "*" or "**" labels, or an IP address, and an optional port.
	sandboxHostRe = regexp.MustCompile(`^(\*\*|\*|[A-Za-z0-9]([A-Za-z0-9-]{0,62})?)(\.(\*|[A-Za-z0-9]([A-Za-z0-9-]{0,62})?))*(:[0-9]{1,5})?$`)
	sandboxVerRe  = regexp.MustCompile(`^[A-Za-z0-9.+_-]{0,64}$`)
)

// Check reports what is wrong with the declaration.
func (s *SandboxCap) Check() error {
	if s == nil {
		return nil
	}
	if !sandboxVerRe.MatchString(s.Version) {
		return fmt.Errorf("sandbox: version %q", s.Version)
	}
	if !sandboxVerRe.MatchString(s.Bin) {
		return fmt.Errorf("sandbox: bin %q", s.Bin)
	}
	if s.Template != "" && !checkSandboxTemplate(s.Template) {
		return fmt.Errorf("sandbox: template %q", s.Template)
	}
	if len(s.Providers) > 2 {
		return errors.New("sandbox: at most 2 providers")
	}
	for _, p := range s.Providers {
		if p != SandboxProviderClaude && p != SandboxProviderCodex {
			return fmt.Errorf("sandbox: provider %q", p)
		}
	}
	if len(s.Problem) > MaxSandboxProblemLen || !utf8.ValidString(s.Problem) || strings.ContainsRune(s.Problem, 0) {
		return errors.New("sandbox: the problem is too long or not text")
	}
	return nil
}

// Ready reports whether brains may run in the node's sandboxes with
// provider: sbx answers, signed in, its settings hold, and the
// provider's login is there.
func (s *SandboxCap) Ready(provider string) bool {
	return s != nil && s.Problem == "" && s.SignedIn && s.Daemon && s.Settings && slices.Contains(s.Providers, provider)
}

// summary is the capability's word, as the dashboard shows it too:
// whether it is ready is in its fields, not in the word.
func (s *SandboxCap) summary() string { return "sandbox" }

func checkSandboxTemplate(t string) bool {
	return len(t) <= 256 && utf8.ValidString(t) && !strings.ContainsAny(t, " \t\r\n\x00") && !strings.HasPrefix(t, "-")
}

// SandboxInfo answers sandbox.info: the capability, the brains'
// sandboxes, the host's free memory, and where fridolin's own binary and
// tools are inside the VM.
type SandboxInfo struct {
	SandboxCap
	Sandboxes    []SandboxVM `json:"sandboxes"`
	FreeMemoryMB int64       `json:"free_memory_mb"`
	// BinPath is FRIDOLIN_BIN inside the VM: the linux fridolin on a
	// read-only mount, which a run's MCP config names.
	BinPath string `json:"bin_path"`
	// ToolsPath is FRIDOLIN_TOOLS_DIR inside the VM.
	ToolsPath string `json:"tools_path"`
}

// SandboxVM is one of fridolin's sandboxes on the node.
type SandboxVM struct {
	Name     string `json:"name"`
	Brain    string `json:"brain"`
	Status   string `json:"status"`
	MemoryMB int    `json:"memory_mb"`
}

// SandboxMount is a host folder in the sandbox, at the same path.
type SandboxMount struct {
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only"`
}

// SandboxEnsureParams makes sure the brain's sandbox exists as asked.
// Folder is mounted read-write; Mounts are the others. Marker is the id
// the core wrote into Folder's SandboxMarkerFile.
type SandboxEnsureParams struct {
	Brain    string         `json:"brain"`
	Folder   string         `json:"folder"`
	Marker   string         `json:"marker"`
	Mounts   []SandboxMount `json:"mounts,omitempty"`
	MemoryMB int            `json:"memory_mb,omitempty"`
	Egress   []string       `json:"egress,omitempty"`
	APIPort  int            `json:"api_port"`
	Provider string         `json:"provider"`
}

// What sandbox.ensure did.
const (
	SandboxCreated   = "created"
	SandboxRecreated = "recreated"
	SandboxReady     = "ready"
)

// SandboxEnsureResult names the sandbox and what ensure did to it.
type SandboxEnsureResult struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// Check reports what is wrong with the call.
func (p SandboxEnsureParams) Check() error {
	if err := checkSandboxBrain(p.Brain); err != nil {
		return err
	}
	if err := checkSandboxFolder("folder", p.Folder); err != nil {
		return err
	}
	if p.Marker == "" || len(p.Marker) > MaxSandboxMarkerLen || !utf8.ValidString(p.Marker) || strings.ContainsAny(p.Marker, "\x00\r\n") {
		return errors.New("sandbox: the marker is empty, too long or not one line")
	}
	if len(p.Mounts) > MaxSandboxMounts {
		return fmt.Errorf("sandbox: at most %d mounts", MaxSandboxMounts)
	}
	for _, m := range p.Mounts {
		if err := checkSandboxFolder("mount", m.Path); err != nil {
			return err
		}
	}
	if p.MemoryMB != 0 && (p.MemoryMB < MinSandboxMemoryMB || p.MemoryMB > MaxSandboxMemoryMB) {
		return fmt.Errorf("sandbox: memory_mb from %d to %d, or 0", MinSandboxMemoryMB, MaxSandboxMemoryMB)
	}
	if err := CheckSandboxEgress(p.Egress); err != nil {
		return err
	}
	if err := checkSandboxPort(p.APIPort); err != nil {
		return err
	}
	return checkSandboxProvider(p.Provider)
}

// CheckSandboxEgress reports what is wrong with a brain's egress hosts.
func CheckSandboxEgress(hosts []string) error {
	if len(hosts) > MaxSandboxEgress {
		return fmt.Errorf("sandbox: at most %d egress hosts", MaxSandboxEgress)
	}
	for _, h := range hosts {
		if len(h) > 253 || !sandboxHostRe.MatchString(h) {
			return fmt.Errorf("sandbox: egress host %q", h)
		}
	}
	return nil
}

func checkSandboxBrain(b string) error {
	if !sandboxBrainRe.MatchString(b) {
		return fmt.Errorf("sandbox: brain %q", b)
	}
	return nil
}

func checkSandboxRun(r string) error {
	if !sandboxRunRe.MatchString(r) {
		return fmt.Errorf("sandbox: run %q", r)
	}
	return nil
}

// checkSandboxFolder holds a host path to what sbx mounts: absolute,
// clean, and with no ":" or "," that its mount syntax would split on.
func checkSandboxFolder(what, p string) error {
	if p == "" || len(p) > maxPathLen || !utf8.ValidString(p) || !strings.HasPrefix(p, "/") || strings.ContainsAny(p, ":,\x00\r\n") {
		return fmt.Errorf("sandbox: %s %q must be an absolute path without : or ,", what, p)
	}
	parts := strings.Split(p, "/")
	if p != "/" && (strings.HasSuffix(p, "/") || strings.Contains(p, "//") || slices.Contains(parts, "..") || slices.Contains(parts, ".")) {
		return fmt.Errorf("sandbox: %s %q is not a clean path", what, p)
	}
	return nil
}

func checkSandboxPort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("sandbox: api_port %d", port)
	}
	return nil
}

func checkSandboxProvider(p string) error {
	if p != SandboxProviderClaude && p != SandboxProviderCodex {
		return fmt.Errorf("sandbox: provider %q", p)
	}
	return nil
}

// SandboxRunParams starts one run in the brain's sandbox, made by
// sandbox.ensure. Argv is the provider's command line, with paths valid
// inside the VM; Env is the run's environment without secrets; Stdin is
// the prompt; Token is the run token, which the node keeps on the host
// and the VM sees only as a placeholder. TimeoutS ends the run inside
// the VM after that long (0: only the core's deadline).
type SandboxRunParams struct {
	Brain    string            `json:"brain"`
	Run      string            `json:"run"`
	Folder   string            `json:"folder"`
	Argv     []string          `json:"argv"`
	Env      map[string]string `json:"env,omitempty"`
	Stdin    string            `json:"stdin,omitempty"`
	Token    string            `json:"token"`
	APIPort  int               `json:"api_port"`
	Provider string            `json:"provider"`
	TimeoutS int               `json:"timeout_s,omitempty"`
	// GitHubToken is the run's GitHub token (docs/GITHUB.md), "" for a
	// run without a grant. The node stores it as the sandbox's own
	// `github` secret while the run goes on, and opens SandboxGitHubHosts
	// for it: sbx's proxy puts it into the run's calls to GitHub, and the
	// VM holds only sbx's placeholder in GH_TOKEN.
	GitHubToken string `json:"github_token,omitempty"`
}

// SandboxGitHubHosts are the hosts a run with a GitHub token may reach:
// the API, git over HTTPS, archives and raw files.
var SandboxGitHubHosts = []string{
	"github.com", "api.github.com", "codeload.github.com", "objects.githubusercontent.com", "raw.githubusercontent.com",
}

// SandboxNodeEnv are the variables the node sets for a run itself, and
// the proxy settings and credentials' placeholders that are the VM's
// own: the core may not send them.
var SandboxNodeEnv = []string{
	"FRIDOLIN_API", "FRIDOLIN_BRAIN_TOKEN_FILE", "FRIDOLIN_BIN", "FRIDOLIN_TOOLS_DIR",
	"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY",
	"GH_TOKEN", "GITHUB_TOKEN", "GH_CONFIG_DIR",
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy",
}

// Check reports what is wrong with the call.
func (p SandboxRunParams) Check() error {
	if err := checkSandboxBrain(p.Brain); err != nil {
		return err
	}
	if err := checkSandboxRun(p.Run); err != nil {
		return err
	}
	if err := checkSandboxFolder("folder", p.Folder); err != nil {
		return err
	}
	if len(p.Argv) == 0 || p.Argv[0] == "" || len(p.Argv) > MaxSandboxArgs {
		return fmt.Errorf("sandbox: argv must have 1 to %d words", MaxSandboxArgs)
	}
	size := 0
	for _, a := range p.Argv {
		if strings.ContainsRune(a, 0) || !utf8.ValidString(a) {
			return errors.New("sandbox: an argument is not text")
		}
		size += len(a)
	}
	if len(p.Env) > MaxSandboxEnv {
		return fmt.Errorf("sandbox: at most %d variables", MaxSandboxEnv)
	}
	for k, v := range p.Env {
		if !sandboxEnvRe.MatchString(k) {
			return fmt.Errorf("sandbox: variable name %q", k)
		}
		if slices.Contains(SandboxNodeEnv, k) {
			return fmt.Errorf("sandbox: the node sets %s itself", k)
		}
		if strings.ContainsRune(v, 0) || !utf8.ValidString(v) {
			return fmt.Errorf("sandbox: variable %s is not text", k)
		}
		size += len(k) + len(v)
	}
	if size > MaxSandboxArgBytes {
		return fmt.Errorf("sandbox: argv and env hold more than %d bytes", MaxSandboxArgBytes)
	}
	if len(p.Stdin) > MaxSandboxStdin {
		return fmt.Errorf("sandbox: stdin holds more than %d bytes", MaxSandboxStdin)
	}
	if p.Token == "" || len(p.Token) > MaxSandboxTokenLen || strings.ContainsAny(p.Token, "\x00\r\n") {
		return errors.New("sandbox: the run token is empty, too long or not one line")
	}
	if len(p.GitHubToken) > MaxSandboxTokenLen || strings.ContainsAny(p.GitHubToken, "\x00\r\n\t ") {
		return errors.New("sandbox: the GitHub token is too long or not one word")
	}
	if err := checkSandboxPort(p.APIPort); err != nil {
		return err
	}
	if p.TimeoutS < 0 || p.TimeoutS > MaxSandboxTimeoutS {
		return fmt.Errorf("sandbox: timeout_s from 0 to %d", MaxSandboxTimeoutS)
	}
	return checkSandboxProvider(p.Provider)
}

// SandboxRunResult says the run started.
type SandboxRunResult struct {
	Run string `json:"run"`
}

// SandboxReadParams reads a run's stdout from Offset, waiting up to
// WaitMS for more when there is none yet and the run goes on.
type SandboxReadParams struct {
	Run    string `json:"run"`
	Offset int64  `json:"offset"`
	WaitMS int    `json:"wait_ms,omitempty"`
}

// Check reports what is wrong with the call.
func (p SandboxReadParams) Check() error {
	if err := checkSandboxRun(p.Run); err != nil {
		return err
	}
	if p.Offset < 0 || p.Offset > SandboxRunBuffer {
		return fmt.Errorf("sandbox: offset from 0 to %d", SandboxRunBuffer)
	}
	if p.WaitMS < 0 || p.WaitMS > MaxSandboxReadWaitMS {
		return fmt.Errorf("sandbox: wait_ms from 0 to %d", MaxSandboxReadWaitMS)
	}
	return nil
}

// SandboxReadResult is a run's stdout from the offset asked to Next, as
// UTF-8 text (never split inside a character). Done says the run ended
// and Data holds the last of it: ExitCode, StderrTail and Error are then
// set. Error is why the run failed on the node (its output went past
// SandboxRunBuffer, sbx could not start it), "" when it ran.
type SandboxReadResult struct {
	Data       string `json:"data"`
	Next       int64  `json:"next"`
	Done       bool   `json:"done"`
	ExitCode   int    `json:"exit_code"`
	StderrTail string `json:"stderr_tail"`
	Error      string `json:"error"`
}

// SandboxStopParams ends a run: TERM to its process group in the VM,
// KILL after GraceS seconds (0: DefaultSandboxGraceS).
type SandboxStopParams struct {
	Run    string `json:"run"`
	GraceS int    `json:"grace_s,omitempty"`
}

// Check reports what is wrong with the call.
func (p SandboxStopParams) Check() error {
	if err := checkSandboxRun(p.Run); err != nil {
		return err
	}
	if p.GraceS < 0 || p.GraceS > MaxSandboxGraceS {
		return fmt.Errorf("sandbox: grace_s from 0 to %d", MaxSandboxGraceS)
	}
	return nil
}

// Grace is the stop's grace, the default filled in.
func (p SandboxStopParams) Grace() time.Duration {
	if p.GraceS == 0 {
		return DefaultSandboxGraceS * time.Second
	}
	return time.Duration(p.GraceS) * time.Second
}

// SandboxRemoveParams removes the brain's sandbox.
type SandboxRemoveParams struct {
	Brain string `json:"brain"`
}

// Check reports what is wrong with the call.
func (p SandboxRemoveParams) Check() error { return checkSandboxBrain(p.Brain) }
