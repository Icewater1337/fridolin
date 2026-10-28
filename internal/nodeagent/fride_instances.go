package nodeagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// FrIDE instances (docs/NODES.md, "FrIDE instances"): whole FrIDEs, each
// in a Docker Sandbox (sbx microVM) of its own, for one trust domain,
// which the core starts and stops on a node through fride.instance.*.
// The node's own agent runs them: `fridolin node fride host setup`
// builds the template (FrIDE, Claude Code and ponytail) once, or with
// --no-build takes the one the core publishes, and the node then offers
// fride_host. On Windows the node's service runs sbx in the signed-in
// user's session (sbx_windows.go). Each instance has its sandbox
// fride-<name>, a pair of loopback ports from 17850 up (FrIDE's and its
// webhook's), a FrIDE paired with the node, its repositories cloned in
// the VM, git identities whose tokens stay on the host (sbx's proxy
// swaps placeholders for them) and a network allowlist. Its state lives
// in <state dir>/fride-instances/<name>/.
//
// The native companion FrIDE (fride.go) runs beside them, as the
// instance "".

// The files and folders of the instances in the node's state dir.
const (
	// frideInstancesDir holds a folder per instance.
	frideInstancesDir = "fride-instances"
	// frideInstanceFile is an instance's state in its folder.
	frideInstanceFile = "instance.json"
	// frideInstanceTokenFile is the token the node paired with the
	// instance's FrIDE (0600).
	frideInstanceTokenFile = "fride.token"
	// frideInstanceTokens holds the instance's git hosts' tokens and
	// placeholders (sbx_tokens.go).
	frideInstanceTokens = "tokens"
	// frideInstanceHistoryDir and frideInstanceCarryDir are the saved
	// history and what a remake carries (fride_instance_history.go).
	frideInstanceHistoryDir = "history"
	frideInstanceCarryDir   = "carry"
	// frideHostFile is what `fridolin node fride host setup` set up.
	frideHostFile = "fride-host.json"
)

// The instances' ports: a pair per instance, FrIDE's and its webhook's,
// from FrideInstancePortBase up. FrIDE's port is the same in the VM and
// on the host's loopback, since FrIDE checks the Host header.
const (
	FrideInstancePortBase = 17850
	frideInstancePortLast = 17999
)

// frideInstanceSandboxPrefix starts an instance's sandbox's name; brains
// never had it (fridolin-<brain>).
const frideInstanceSandboxPrefix = "fride-"

// FrideInstanceSandbox is the sandbox of the instance name.
func FrideInstanceSandbox(name string) string { return frideInstanceSandboxPrefix + name }

// frideInstanceSandboxRe is an instance's sandbox's name.
var frideInstanceSandboxRe = regexp.MustCompile(`^fride-[a-z][a-z0-9-]{0,23}$`)

// FrideInstanceState is instance.json: how the instance was made, and
// what the node gave it.
type FrideInstanceState struct {
	Name    string `json:"name"`
	Sandbox string `json:"sandbox"`
	// MemoryMB is the sandbox's memory, Template what it was made from.
	MemoryMB int    `json:"memory_mb"`
	Template string `json:"template"`
	// Port is FrIDE's, WebhookPort the one the node hears its news on.
	Port        int `json:"port"`
	WebhookPort int `json:"webhook_port"`
	// DockerDisk is the size of its Docker disk, Caches the host folder of
	// the shared caches ("" for none), both from the host's setup when the
	// sandbox was made.
	DockerDisk string `json:"docker_disk,omitempty"`
	Caches     string `json:"caches,omitempty"`
	// Run says the agent keeps FrIDE running (start sets it, stop clears
	// it).
	Run bool `json:"run"`
	// Repos are the git URLs the core asked for last.
	Repos []string `json:"repos,omitempty"`
	// Rules are the network rules the sandbox has (sbx policy allow).
	Rules []string `json:"rules,omitempty"`
	// Identities are the git identities FrIDE has, without their tokens;
	// Pushed is the fingerprint of what it took.
	Identities []FrideInstanceIdentity `json:"identities,omitempty"`
	Pushed     string                  `json:"pushed,omitempty"`
	// Problem says why the last start failed; "" when it did not. Notes
	// say what did not work in the last start that did (a repository that
	// did not clone, no Claude login), for as long as it runs.
	Problem string    `json:"problem,omitempty"`
	Notes   string    `json:"notes,omitempty"`
	Made    time.Time `json:"made,omitzero"`
	Started time.Time `json:"started,omitzero"`
}

// FrideInstanceIdentity is a git identity an instance has: never its
// token.
type FrideInstanceIdentity struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Host  string `json:"host"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// frideInstanceDir is the folder of the instance name. Each name is
// checked where it comes in; one that is no instance name anyway gets a
// folder that holds nothing, never a path outside the instances folder.
func frideInstanceDir(dir, name string) string {
	if nodeproto.FrideInstanceName.MatchString(name) {
		return filepath.Join(dir, frideInstancesDir, name)
	}
	return filepath.Join(dir, frideInstancesDir, "_not-an-instance")
}

// frideInstanceURL is FrIDE's address on the host for port.
func frideInstanceURL(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) }

// errNoFrideInstance is an instance the node does not have.
var errNoFrideInstance = errors.New("no such FrIDE instance on this node")

// loadFrideInstance reads the instance name's state; errNoFrideInstance
// for none.
func loadFrideInstance(dir, name string) (*FrideInstanceState, error) {
	if err := nodeproto.CheckFrideInstanceName(name); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(frideInstanceDir(dir, name), frideInstanceFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", errNoFrideInstance, name)
	}
	if err != nil {
		return nil, err
	}
	var st FrideInstanceState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("%s of %s: %w", frideInstanceFile, name, err)
	}
	if st.Name != name || st.Sandbox != FrideInstanceSandbox(name) || st.Port <= 0 || st.WebhookPort <= 0 {
		return nil, fmt.Errorf("%s of %s is not an instance's state", frideInstanceFile, name)
	}
	return &st, nil
}

// saveFrideInstance writes the instance's state.
func saveFrideInstance(dir string, st *FrideInstanceState) error {
	d := frideInstanceDir(dir, st.Name)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(d, frideInstanceFile), append(b, '\n'), 0o600)
}

// listFrideInstances reads every instance's state in dir, by name; one
// that does not read is left out.
func listFrideInstances(dir string) []*FrideInstanceState {
	entries, _ := os.ReadDir(filepath.Join(dir, frideInstancesDir))
	var out []*FrideInstanceState
	for _, e := range entries {
		if !e.IsDir() || nodeproto.CheckFrideInstanceName(e.Name()) != nil {
			continue
		}
		if st, err := loadFrideInstance(dir, e.Name()); err == nil {
			out = append(out, st)
		}
	}
	return out
}

// readInstanceToken is the token the node paired with the instance's
// FrIDE, "" for none.
func readInstanceToken(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(frideInstanceDir(dir, name), frideInstanceTokenFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// freePortPair is the first pair of ports from base up that no instance
// in dir has, and that nothing listens on now.
func freePortPair(dir string, base int) (int, int, error) {
	if base <= 0 {
		base = FrideInstancePortBase
	}
	var taken []int
	for _, st := range listFrideInstances(dir) {
		taken = append(taken, st.Port, st.WebhookPort)
	}
	last := max(frideInstancePortLast, base+149)
	for p := base; p+1 <= last; p += 2 {
		if slices.Contains(taken, p) || slices.Contains(taken, p+1) || !portFree(p) || !portFree(p+1) {
			continue
		}
		return p, p + 1, nil
	}
	return 0, 0, fmt.Errorf("no free pair of ports from %d to %d for another FrIDE instance", base, last)
}

// portFree reports whether nothing listens on the loopback's port.
func portFree(port int) bool {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// FrideHostState is fride-host.json: what `fridolin node fride host
// setup` set up on the machine. The node offers fride_host while it is
// there.
type FrideHostState struct {
	// Template is the sbx template instances are made from, which setup
	// built here: FrIDE at FrideCommit from FrideRepo, Claude Code and
	// ponytail. "" when FromCore.
	Template    string `json:"template"`
	FrideRepo   string `json:"fride_repo,omitempty"`
	FrideCommit string `json:"fride_commit,omitempty"`
	// FromCore says the node built no template (setup --no-build): each
	// start names the core's published one, which sbx pulls.
	FromCore bool `json:"template_from_core,omitempty"`
	// Companion is the FrIDE config a new instance starts from
	// (container/fride/companion.json); nil for the template's own
	// (/opt/fride/companion.json), which a template from the core has.
	Companion json.RawMessage `json:"companion,omitempty"`
	// DockerDisk is the size of a new sandbox's Docker disk, Caches the
	// host folder of the shared Go and npm caches ("" for none).
	DockerDisk string    `json:"docker_disk"`
	Caches     string    `json:"caches,omitempty"`
	SetUp      time.Time `json:"set_up,omitzero"`
}

// LoadFrideHost reads fride-host.json; nil before setup.
func LoadFrideHost(dir string) (*FrideHostState, error) {
	b, err := os.ReadFile(filepath.Join(dir, frideHostFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var h FrideHostState
	if err := json.Unmarshal(b, &h); err != nil {
		return nil, fmt.Errorf("%s: %w", frideHostFile, err)
	}
	if h.Template == "" && !h.FromCore || h.Template != "" && !json.Valid(h.Companion) {
		return nil, fmt.Errorf("%s: no template or FrIDE config: run fridolin node fride host setup again", frideHostFile)
	}
	return &h, nil
}

// companion is the FrIDE config a new instance's prepare script gets on
// stdin: nil for the template's own.
func (h *FrideHostState) companion() []byte {
	if c := bytes.TrimSpace(h.Companion); len(c) > 0 && string(c) != "null" {
		return c
	}
	return nil
}

// Pull policies of sbx create: the node's own template is in sbx's store
// (setup loaded it); the core's is pulled when sbx lacks it.
const (
	sbxPullNever   = "never"
	sbxPullMissing = "missing"
)

// errNoTemplate is a start that names no template on a node that built
// none.
var errNoTemplate = errors.New("this node built no FrIDE template (fridolin node fride host setup --no-build), and the core sends none: " +
	"it does not know where its images are published (FRIDOLIN_BOX_REGISTRY); set the core up from its published image, or run fridolin node fride host setup with Docker here")

// templateFor is the template a new sandbox is made from, and sbx's pull
// policy for it: the node's own when setup built one, else core, the
// one the start names.
func (h *FrideHostState) templateFor(core string) (tpl, pull string, err error) {
	switch {
	case h.Template != "":
		return h.Template, sbxPullNever, nil
	case core != "":
		return core, sbxPullMissing, nil
	}
	return "", "", errNoTemplate
}

// pullFor is sbx's pull policy for tpl: never for the node's own.
func (h *FrideHostState) pullFor(tpl string) string {
	if h.Template != "" && tpl == h.Template {
		return sbxPullNever
	}
	return sbxPullMissing
}

// saveFrideHost writes fride-host.json.
func saveFrideHost(dir string, h FrideHostState) error {
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, frideHostFile), append(b, '\n'), 0o600)
}

// FrideHostConfig is node.json's fride_host section.
type FrideHostConfig struct {
	// ReserveMB is the memory kept for the machine itself, which no
	// instance is started into (DefaultFrideReserveMB when 0).
	ReserveMB int `json:"reserve_mb,omitempty"`
}

// DefaultFrideReserveMB is the memory a fride_host node keeps for
// itself unless node.json says otherwise.
const DefaultFrideReserveMB = 6144

// Check reports what is wrong with the section.
func (c *FrideHostConfig) Check() error {
	if c != nil && (c.ReserveMB < 0 || c.ReserveMB > nodeproto.MaxFrideInstanceMemoryMB*4) {
		return fmt.Errorf("fride_host.reserve_mb: %d is not between 0 and %d", c.ReserveMB, nodeproto.MaxFrideInstanceMemoryMB*4)
	}
	return nil
}

// reserveMB is what the node keeps for itself.
func (c Config) reserveMB() int {
	if c.FrideHost != nil && c.FrideHost.ReserveMB > 0 {
		return c.FrideHost.ReserveMB
	}
	return DefaultFrideReserveMB
}
