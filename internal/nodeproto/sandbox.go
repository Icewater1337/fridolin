package nodeproto

import (
	"fmt"
	"strings"
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
}

// SandboxApproval is what approving sandbox means, in the owner's words.
const SandboxApproval = "runs brains in Docker Sandboxes on this machine"

// The sandbox methods. Each needs the sandbox capability. Request and
// response bodies are the Sandbox* types below.
const (
	// MethodSandboxInfo reports sbx and the sandboxes (nothing → SandboxInfo).
	MethodSandboxInfo = "sandbox.info"
	// MethodSandboxEnsure makes a brain's sandbox, or remakes it when its
	// mounts changed (SandboxEnsureParams → SandboxEnsureResult). A
	// CodeUnavailable error means not available here (the core falls
	// back), CodeFailed that the setup failed (the core refuses the run).
	MethodSandboxEnsure = "sandbox.ensure"
	// MethodSandboxRun starts a run (SandboxRunParams → SandboxRunResult).
	MethodSandboxRun = "sandbox.run"
	// MethodSandboxRead reads a run's output, waiting for more up to
	// WaitMS (SandboxReadParams → SandboxReadResult).
	MethodSandboxRead = "sandbox.read"
	// MethodSandboxStop ends a run inside the VM: TERM to its process
	// group, KILL after the grace (SandboxStopParams → nothing).
	MethodSandboxStop = "sandbox.stop"
	// MethodSandboxRemove removes a brain's sandbox (SandboxRemoveParams →
	// nothing).
	MethodSandboxRemove = "sandbox.remove"
)

// SandboxMethods are the methods the sandbox capability allows.
var SandboxMethods = []string{MethodSandboxInfo, MethodSandboxEnsure, MethodSandboxRun, MethodSandboxRead, MethodSandboxStop, MethodSandboxRemove}

// Bounds of the sandbox methods.
const (
	// MaxSandboxStdinBytes bounds a run's prompt.
	MaxSandboxStdinBytes = 4 << 20
	// MaxSandboxOutputBytes is what the node buffers of a run's stdout.
	MaxSandboxOutputBytes = 16 << 20
	// MaxSandboxReadWaitMS bounds a read's long poll.
	MaxSandboxReadWaitMS = 20_000
	// MaxSandboxEgress bounds a brain's extra hosts.
	MaxSandboxEgress = 50
	// SandboxForgetMinutes is how long after its last read a run that
	// ended is forgotten.
	SandboxForgetMinutes = 10
	maxSandboxProviders  = 8
	maxSandboxText       = 1024
)

// SandboxName is the sandbox of brain on the node.
func SandboxName(brain string) string { return "fridolin-" + brain }

// Check reports what is wrong with the declaration.
func (c *SandboxCap) Check() error {
	if c == nil {
		return nil
	}
	if len(c.Providers) > maxSandboxProviders {
		return fmt.Errorf("sandbox: at most %d providers", maxSandboxProviders)
	}
	for _, s := range append([]string{c.Version, c.Template, c.Problem}, c.Providers...) {
		if len(s) > maxSandboxText || !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return fmt.Errorf("sandbox: a field longer than %d or not text", maxSandboxText)
		}
	}
	return nil
}

// Ready says why the sandbox cannot run provider's brains now, "" when it
// can.
func (c *SandboxCap) Ready(provider string) string {
	if why := c.NotReady(); why != "" {
		return why
	}
	for _, p := range c.Providers {
		if p == provider {
			return ""
		}
	}
	return provider + " has no login in Docker Sandboxes on the node"
}

// NotReady says why the sandbox cannot run any brain now, "" when sbx is
// ready (a provider's login aside).
func (c *SandboxCap) NotReady() string {
	switch {
	case c == nil:
		return "the node offers no Docker Sandboxes"
	case c.Problem != "":
		return c.Problem
	case !c.Daemon:
		return "sbx's daemon does not answer"
	case !c.SignedIn:
		return "sbx is not signed in to Docker"
	case !c.Settings:
		return "sbx's global settings are not Fridolin's"
	case c.Template == "":
		return "the brain template is not set up (fridolin node sandbox setup)"
	}
	return ""
}

// SandboxInfo answers sandbox.info.
type SandboxInfo struct {
	SandboxCap
	Sandboxes    []SandboxState `json:"sandboxes"`
	FreeMemoryMB int64          `json:"free_memory_mb"`
	// BinPath is the VM's fridolin, on a read-only mount, and ToolsPath
	// the VM's tools/: the core names them in a run's MCP config and in
	// FRIDOLIN_BIN and FRIDOLIN_TOOLS_DIR. The node does not rewrite the
	// MCP config.
	BinPath   string `json:"bin_path"`
	ToolsPath string `json:"tools_path"`
}

// SandboxState is one brain sandbox on the node.
type SandboxState struct {
	Name     string `json:"name"`
	Brain    string `json:"brain"`
	Status   string `json:"status"`
	MemoryMB int64  `json:"memory_mb"`
}

// SandboxMount is a host folder mounted at the same path in the VM.
type SandboxMount struct {
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only"`
}

// SandboxEnsureParams asks for brain's sandbox. Marker is what the core
// wrote to the folder's .fridolin-runtime-id: the node refuses
// (CodeUnavailable) when the file there holds anything else.
type SandboxEnsureParams struct {
	Brain    string         `json:"brain"`
	Folder   string         `json:"folder"`
	Marker   string         `json:"marker"`
	Mounts   []SandboxMount `json:"mounts"`
	MemoryMB int            `json:"memory_mb"`
	Egress   []string       `json:"egress"`
	APIPort  int            `json:"api_port"`
	Provider string         `json:"provider"`
}

// SandboxEnsureResult answers sandbox.ensure: State is created,
// recreated or ready.
type SandboxEnsureResult struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// SandboxRunParams starts a run in brain's sandbox. Argv is the
// provider's command line with paths valid in the VM; Env the run's
// environment without secrets. Token is the run's brain API token: it
// goes only here, never in Env or Argv. The node adds the provider's
// login, FRIDOLIN_API (host.docker.internal:APIPort) and
// FRIDOLIN_BRAIN_TOKEN_FILE (the placeholder file).
type SandboxRunParams struct {
	Brain    string            `json:"brain"`
	Run      string            `json:"run"`
	Folder   string            `json:"folder"`
	Argv     []string          `json:"argv"`
	Env      map[string]string `json:"env"`
	Stdin    string            `json:"stdin"`
	Token    string            `json:"token"`
	APIPort  int               `json:"api_port"`
	Provider string            `json:"provider"`
	TimeoutS int               `json:"timeout_s"`
}

// SandboxRunResult answers sandbox.run: the run has started.
type SandboxRunResult struct {
	Run string `json:"run"`
}

// SandboxReadParams reads run's stdout from Offset.
type SandboxReadParams struct {
	Run    string `json:"run"`
	Offset int64  `json:"offset"`
	WaitMS int    `json:"wait_ms"`
}

// SandboxReadResult answers sandbox.read: Data is stdout from the offset,
// Next the offset after it. Once Done, ExitCode, StderrTail and Error
// (the node's own failure, "" for none) say how the run ended.
type SandboxReadResult struct {
	Data       string `json:"data"`
	Next       int64  `json:"next"`
	Done       bool   `json:"done"`
	ExitCode   int    `json:"exit_code"`
	StderrTail string `json:"stderr_tail"`
	Error      string `json:"error"`
}

// SandboxStopParams ends run, killing it GraceS seconds after TERM.
type SandboxStopParams struct {
	Run    string `json:"run"`
	GraceS int    `json:"grace_s"`
}

// SandboxRemoveParams removes brain's sandbox.
type SandboxRemoveParams struct {
	Brain string `json:"brain"`
}
