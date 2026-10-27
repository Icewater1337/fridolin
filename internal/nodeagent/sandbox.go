package nodeagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// Docker Sandboxes on the node (#149): the agent declares sandbox once
// `fridolin node sandbox setup` recorded a template, while it finds sbx
// and sbx answers, and runs brains' runs in one microVM per
// brain, fridolin-<brain>, as the core asks (sandbox.* methods).

// Files of the sandbox in the state dir.
const (
	// sandboxOffFile turns the capability off on this machine
	// (`fridolin node sandbox off`).
	sandboxOffFile = "sandbox-off"
	// sandboxDir holds the state file and the runs' token files.
	sandboxDir = "sandbox"
	// sandboxStateFile is what the agent keeps of its sandboxes.
	sandboxStateFile = "state.json"
)

// Paths inside the VM: fridolin's linux binary and its tools, on
// read-only mounts (sandbox.info reports them).
const (
	sandboxBinPath   = "/opt/fridolin/bin/fridolin"
	sandboxToolsPath = "/opt/fridolin/tools"
)

// sandboxCheckEvery is how often the agent asks sbx again (at most; it
// asks at each check of FrIDE that is due).
const sandboxCheckEvery = 5 * time.Minute

// sandbox is the agent's Docker Sandboxes.
type sandbox struct {
	mu      sync.Mutex
	cap     *nodeproto.SandboxCap
	checked time.Time
	lastErr string
	// ensure serializes creating and removing sandboxes, and state.json.
	ensure sync.Mutex
	runs   map[string]*sandboxRun
}

// sandboxState is state.json: the template fridolin made, and per brain
// what its sandbox was made with.
type sandboxState struct {
	Template  string                   `json:"template,omitempty"`
	Sandboxes map[string]sandboxRecord `json:"sandboxes,omitempty"`
}

// sandboxRecord is one brain's sandbox: the key of its mounts, memory,
// template, egress, API port and provider, and its memory.
type sandboxRecord struct {
	Key      string `json:"key"`
	MemoryMB int    `json:"memory_mb"`
}

func (st sandboxState) memory(brain string) int { return st.Sandboxes[brain].MemoryMB }

// SandboxOff reports whether the sandbox capability is turned off in the
// node's state dir.
func SandboxOff(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, sandboxOffFile))
	return err == nil
}

// SetSandboxOff turns the sandbox capability off (or on again) in the
// node's state dir; the running agent sees it at its next check.
func SetSandboxOff(dir string, off bool) error {
	p := filepath.Join(dir, sandboxOffFile)
	if !off {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.WriteFile(p, []byte("Docker Sandboxes are turned off here: fridolin node sandbox on\n"), 0o600)
}

// sandboxOffered reports whether the agent may offer sandbox: on macOS
// and Linux, unless turned off here.
func (a *Agent) sandboxOffered() bool {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return false
	}
	return a.Dir == "" || !SandboxOff(a.Dir)
}

// sbx is the driver of sbx, nil without one.
func (a *Agent) sbx() sbxDriver {
	if a.SandboxDriver != nil {
		return a.SandboxDriver
	}
	if a.SandboxSbx == "" {
		return nil
	}
	return sbxCLI{path: a.SandboxSbx}
}

func (a *Agent) sandboxStatePath() string {
	return filepath.Join(a.Dir, sandboxDir, sandboxStateFile)
}

// loadSandboxState reads state.json; none is an empty state.
func (a *Agent) loadSandboxState() (sandboxState, error) {
	var st sandboxState
	if a.Dir == "" {
		return st, nil
	}
	b, err := os.ReadFile(a.sandboxStatePath())
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("%s: %w", a.sandboxStatePath(), err)
	}
	return st, nil
}

// saveSandboxState writes state.json.
func (a *Agent) saveSandboxState(st sandboxState) error {
	if a.Dir == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(a.Dir, sandboxDir), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(a.sandboxStatePath(), append(b, '\n'), 0o600)
}
