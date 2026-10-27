package nodeagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FrIDE in a Docker Sandbox (docs/NODES.md, "FrIDE in a Docker
// Sandbox"): a whole FrIDE, with its coding agents, in one sbx microVM
// on the node. `fridolin node fride sandbox create` makes the sandbox
// and builds FrIDE inside at the commit fridolin pins; the agent keeps
// `fride serve` running there while fride-sandbox.json says run (an
// sbx exec held open, or the VM stops 30 s after its last session); the
// node pairs with it like with any FrIDE, on loopback ports of its own.
//
// It runs beside the native companion FrIDE (17840/17841), on a node of
// its own: a node declares one FrIDE, so the sandboxed one is paired to
// a second node identity in a state dir of its own, with a service of
// its own (Config.Service).

// The sandboxed FrIDE's names and ports. Its sandbox is not named
// fridolin-<brain>, so that no brain's sandbox can ever be it.
const (
	// FrideSandboxName is the sandbox.
	FrideSandboxName = "fride-sandbox"
	// FrideSandboxPort is FrIDE's port, the same in the VM and on the
	// host's loopback: FrIDE checks the Host header.
	FrideSandboxPort = 17850
	// FrideSandboxWebhookPort is where the node hears FrIDE's webhook on
	// the host's loopback; a forwarder in the VM carries it there.
	FrideSandboxWebhookPort = 17851
	// FrideSandboxService is the node's service (Config.Service).
	FrideSandboxService = "fride-sandbox"
	// frideSandboxMemory is the sandbox's memory.
	frideSandboxMemory = "6g"
	// frideSandboxFile is the sandbox's state in the node's state dir.
	frideSandboxFile = "fride-sandbox.json"
	// frideSandboxTemplateRepo is the template's image name.
	frideSandboxTemplateRepo = "fridolin-fride-sandbox"
	// frideSandboxDockerfile is the template's Dockerfile in a checkout.
	frideSandboxDockerfile = "container/sandbox/fride.Dockerfile"
	// frideImageDockerfile pins FrIDE's commit (container/fride).
	frideImageDockerfile = "container/fride/Dockerfile"
	// frideCompanionConfig is the box's FrIDE config, which the
	// sandbox's starts from.
	frideCompanionConfig = "container/fride/companion.json"
)

// frideSandboxURL is FrIDE's address on the host.
var frideSandboxURL = "http://127.0.0.1:" + strconv.Itoa(FrideSandboxPort)

// frideSandboxEgress is what the sandbox may reach besides the kit's
// Claude hosts: GitHub, Go's module proxy and checksum database, npm,
// and Anthropic's API (docs/SANDBOXES-SPIKE.md, "FrIDE in a Docker
// Sandbox").
var frideSandboxEgress = []string{
	"github.com:443", "api.github.com:443", "codeload.github.com:443",
	"objects.githubusercontent.com:443", "raw.githubusercontent.com:443",
	"proxy.golang.org:443", "sum.golang.org:443", "storage.googleapis.com:443",
	"registry.npmjs.org:443",
	"api.anthropic.com:443",
}

// FrideSandboxState is fride-sandbox.json: what the sandbox was made
// with, the FrIDE built in it, and whether the agent keeps it running.
type FrideSandboxState struct {
	Name     string `json:"name"`
	Base     string `json:"base"`
	Template string `json:"template"`
	// Repo is FrIDE's git URL, Commit what was built from it.
	Repo   string    `json:"fride_repo"`
	Commit string    `json:"commit,omitempty"`
	Built  time.Time `json:"built,omitzero"`
	// Run says the agent keeps FrIDE running (start; stop clears it).
	Run bool `json:"run"`
}

// LoadFrideSandbox reads fride-sandbox.json; nil for none.
func LoadFrideSandbox(dir string) (*FrideSandboxState, error) {
	b, err := os.ReadFile(filepath.Join(dir, frideSandboxFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st FrideSandboxState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", frideSandboxFile, err)
	}
	if st.Name != FrideSandboxName || !filepath.IsAbs(st.Base) {
		return nil, fmt.Errorf("%s: not a sandboxed FrIDE's state", frideSandboxFile)
	}
	return &st, nil
}

// saveFrideSandbox writes fride-sandbox.json.
func saveFrideSandbox(dir string, st FrideSandboxState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, frideSandboxFile), append(b, '\n'), 0o600)
}

// removeFrideSandbox removes fride-sandbox.json.
func removeFrideSandbox(dir string) error {
	err := os.Remove(filepath.Join(dir, frideSandboxFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// commitRe is a full git commit id.
var commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// FridePin is the FrIDE commit the checkout at repo pins: the
// fridolin-fride image's FRIDE_COMMIT.
func FridePin(repo string) (string, error) {
	return dockerfileArg(filepath.Join(repo, filepath.FromSlash(frideImageDockerfile)), "FRIDE_COMMIT", commitRe)
}

// dockerfileArg is the default of ARG name in the Dockerfile at path,
// which must match re.
func dockerfileArg(path, name string, re *regexp.Regexp) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("not a fridolin checkout: %w", err)
	}
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ARG "+name+"="); ok {
			if !re.MatchString(v) {
				return "", fmt.Errorf("%s: ARG %s=%q is not what it should be", path, name, v)
			}
			return v, nil
		}
	}
	return "", fmt.Errorf("%s names no ARG %s", path, name)
}

// checkFrideSandboxPair refuses a node already paired with another
// FrIDE: pairing the sandboxed one would replace it.
func checkFrideSandboxPair(cfg Config, url string) error {
	if cfg.Fride != nil && cfg.Fride.URL != url {
		return fmt.Errorf("this node is paired with the FrIDE at %s, and a node pairs with one FrIDE: the sandboxed FrIDE needs a node of its own (fridolin node fride sandbox join, with its own --state-dir)", cfg.Fride.URL)
	}
	return nil
}
