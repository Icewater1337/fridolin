package nodeproto

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// FrideHostCap (fride_host) is a node that runs FrIDE instances: each a
// whole FrIDE in a Docker Sandbox of its own, for one trust domain
// (docs/VISION.md, "FrIDE instances"). A native node declares it once
// `fridolin node fride host setup` ran there; a box never does.
type FrideHostCap struct {
	// Version is sbx's.
	Version string `json:"version"`
	// Ready says sbx is signed in, its daemon runs, fridolin's global
	// settings hold and the FrIDE template is built, or comes from the
	// core (NeedsTemplate).
	Ready   bool   `json:"ready"`
	Problem string `json:"problem,omitempty"`
	// TotalMemoryMB is the machine's memory.
	TotalMemoryMB int `json:"total_memory_mb"`
	// AvailableMemoryMB is what the OS can give now (on macOS free,
	// inactive, purgeable and speculative pages), not its free pages.
	AvailableMemoryMB int `json:"available_memory_mb"`
	// ReserveMB is kept for the machine itself: no instance is started
	// into it.
	ReserveMB int `json:"reserve_mb"`
	// NeedsTemplate says the node built no template of its own (host
	// setup --no-build, the default where no Docker is found): each start
	// names the image to make a sandbox from (FrideInstanceStartParams.
	// Template), which sbx pulls. A core that knows none places no
	// instance there.
	NeedsTemplate bool `json:"needs_template,omitempty"`
	// Instances are the instances the node has, running or not.
	Instances []FrideInstance `json:"instances,omitempty"`
}

// FrideInstance is one FrIDE instance on a node.
type FrideInstance struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	MemoryMB int    `json:"memory_mb"`
	// URL is its FrIDE's loopback address on the node, while it runs.
	URL string `json:"url,omitempty"`
	// Repos are the repositories its FrIDE knows.
	Repos   []string `json:"repos,omitempty"`
	Problem string   `json:"problem,omitempty"`
}

// The states of a FrIDE instance.
const (
	FrideInstanceStarting = "starting"
	FrideInstanceRunning  = "running"
	FrideInstanceStopped  = "stopped"
	FrideInstanceFailed   = "failed"
)

var frideInstanceStates = []string{FrideInstanceStarting, FrideInstanceRunning, FrideInstanceStopped, FrideInstanceFailed}

// FrideInstanceName is what an instance may be called: also the name of
// its sandbox (fride-<name>) and a folder on the node.
var FrideInstanceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)

// CheckFrideInstanceName reports whether name may name an instance.
// "native" is reserved for the node's own FrIDE, which is the instance
// "" in a fride call.
func CheckFrideInstanceName(name string) error {
	if !FrideInstanceName.MatchString(name) || name == "native" {
		return fmt.Errorf("a FrIDE instance's name is a lower-case letter, then up to 23 lower-case letters, digits and dashes, and not %q: %q", "native", name)
	}
	return nil
}

// Bounds of a fride_host declaration.
const (
	maxFrideInstances = 32
	maxProblemLen     = 1024
)

// Check reports what is wrong with the declaration.
func (f *FrideHostCap) Check() error {
	if f == nil {
		return nil
	}
	if len(f.Version) > 64 || len(f.Problem) > maxProblemLen || !utf8.ValidString(f.Problem) {
		return errors.New("fride_host: version or problem too long")
	}
	if f.TotalMemoryMB < 0 || f.AvailableMemoryMB < 0 || f.ReserveMB < 0 {
		return errors.New("fride_host: negative memory")
	}
	if len(f.Instances) > maxFrideInstances {
		return fmt.Errorf("fride_host: at most %d instances", maxFrideInstances)
	}
	seen := map[string]bool{}
	for _, in := range f.Instances {
		if err := CheckFrideInstanceName(in.Name); err != nil {
			return fmt.Errorf("fride_host: %w", err)
		}
		if seen[in.Name] {
			return fmt.Errorf("fride_host: instance %q twice", in.Name)
		}
		seen[in.Name] = true
		if !slices.Contains(frideInstanceStates, in.State) {
			return fmt.Errorf("fride_host: instance %s: state %q", in.Name, in.State)
		}
		if in.MemoryMB < 0 || len(in.Problem) > maxProblemLen || !utf8.ValidString(in.Problem) {
			return fmt.Errorf("fride_host: instance %s: memory or problem", in.Name)
		}
		if in.URL != "" {
			if err := CheckLoopbackURL(in.URL); err != nil {
				return fmt.Errorf("fride_host: instance %s: %w", in.Name, err)
			}
		}
		if len(in.Repos) > maxFrideRepos {
			return fmt.Errorf("fride_host: instance %s: at most %d repositories", in.Name, maxFrideRepos)
		}
		for _, r := range in.Repos {
			if r == "" || len(r) > maxFrideRepoLen || !utf8.ValidString(r) || strings.ContainsAny(r, "\x00\r\n") {
				return fmt.Errorf("fride_host: instance %s: repository name %q", in.Name, r)
			}
		}
	}
	return nil
}

// Instance returns the instance called name, or nil.
func (f *FrideHostCap) Instance(name string) *FrideInstance {
	if f == nil {
		return nil
	}
	for i := range f.Instances {
		if f.Instances[i].Name == name {
			return &f.Instances[i]
		}
	}
	return nil
}

// clone is a deep copy.
func (f *FrideHostCap) clone() *FrideHostCap {
	c := *f
	c.Instances = slices.Clone(f.Instances)
	for i := range c.Instances {
		c.Instances[i].Repos = slices.Clone(c.Instances[i].Repos)
	}
	return &c
}

// The fride_host methods (core → node). Each needs the fride_host
// capability.
const (
	// MethodFrideInstanceStart starts an instance, making its sandbox
	// first when there is none, and returns it once its FrIDE answers and
	// is paired (FrideInstanceStartParams → FrideInstance). It is
	// idempotent: on a running instance of the same memory it only brings
	// repositories, identities and egress up to date; another memory
	// remakes the sandbox at that size, keeping its workspaces, sessions,
	// tasks, login and git config.
	MethodFrideInstanceStart = "fride.instance.start"
	// MethodFrideInstanceStop stops an instance's VM; its disk stays
	// (FrideInstanceRef → FrideInstance).
	MethodFrideInstanceStop = "fride.instance.stop"
	// MethodFrideInstanceRemove saves an instance's history and removes
	// its sandbox (FrideInstanceRef → nothing).
	MethodFrideInstanceRemove = "fride.instance.remove"
	// MethodFrideInstanceStatus lists the instances (nothing →
	// FrideInstances).
	MethodFrideInstanceStatus = "fride.instance.status"
)

// FrideHostMethods lists the fride_host methods.
var FrideHostMethods = []string{MethodFrideInstanceStart, MethodFrideInstanceStop, MethodFrideInstanceRemove, MethodFrideInstanceStatus}

// FrideOn is which FrIDE a fride call is for, embedded in each fride
// method's params: "" is the node's own FrIDE (the fride capability),
// another name one of its instances (fride_host).
type FrideOn struct {
	Instance string `json:"instance,omitempty"`
}

// FrideInstanceRef names an instance.
type FrideInstanceRef struct {
	Name string `json:"name"`
}

// FrideInstances is what fride.instance.status answers.
type FrideInstances struct {
	Instances []FrideInstance `json:"instances"`
}

// FrideInstanceStartParams starts an instance.
type FrideInstanceStartParams struct {
	Name     string `json:"name"`
	MemoryMB int    `json:"memory_mb"`
	// Repos are git URLs, cloned inside the instance when missing.
	Repos []string `json:"repos,omitempty"`
	// Identities are the git identities the instance's FrIDE gets. Their
	// tokens stay on the node: the instance sees placeholders, which
	// sbx's proxy swaps for them.
	Identities []FrideIdentity `json:"identities,omitempty"`
	// Egress are hosts the instance may reach beyond its baseline.
	Egress []string `json:"egress,omitempty"`
	// Git is the commit name and email for workspaces of no identity.
	Git *FrideGitUser `json:"git,omitempty"`
	// Template is the image a new sandbox is made from when the node
	// built none of its own: the core's published FrIDE template
	// (<registry>/fridolin-fride-sandbox:<tag>), which sbx pulls when it
	// lacks it. "" when the core does not know where its images are
	// published (a local build); a node's own template always wins.
	Template string `json:"template,omitempty"`
	// UpdateOnly brings an instance that runs up to date and starts none:
	// one that is stopped stays so, and the answer says it is. The core's
	// refresh of identities sends it, so that it never undoes a stop
	// whose declaration has yet to arrive.
	UpdateOnly bool `json:"update_only,omitempty"`
}

// FrideSandboxImage is the name of the published FrIDE template under
// the core's registry (.github/workflows/image.yml).
const FrideSandboxImage = "fridolin-fride-sandbox"

// templateRefRE is an image a sandbox may be made from: a registry path
// with a tag, a digest or both, never an option.
var templateRefRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(?:\.[a-z0-9._-]+)*(?::[0-9]+)?(?:/[a-z0-9][a-z0-9._-]*)+(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(?:@sha256:[0-9a-f]{64})?$`)

// CheckTemplateRef reports what is wrong with an image a sandbox is to
// be made from.
func CheckTemplateRef(ref string) error {
	if len(ref) > 512 || !templateRefRE.MatchString(ref) || !strings.ContainsAny(ref[strings.LastIndexByte(ref, '/')+1:], ":@") {
		return fmt.Errorf("template %q is not an image on a registry with a tag or digest (like ghcr.io/owner/%s:v1.2.3)", ref, FrideSandboxImage)
	}
	return nil
}

// FrideIdentity is a git identity for an instance, with its token.
type FrideIdentity struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Host  string `json:"host"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	Token string `json:"token,omitempty"`
}

// FrideGitUser is a commit name and email.
type FrideGitUser struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Bounds of fride.instance.start.
const (
	maxInstanceIdentities = 16
	maxInstanceEgress     = 64
	maxHostLen            = 253
	maxTokenLen           = 4096
	// MaxFrideInstanceMemoryMB bounds an instance's memory.
	MaxFrideInstanceMemoryMB = 65536
	// MinFrideInstanceMemoryMB is the least an instance gets.
	MinFrideInstanceMemoryMB = 2048
)

// Check reports what is wrong with the params.
func (p FrideInstanceStartParams) Check() error {
	if err := CheckFrideInstanceName(p.Name); err != nil {
		return err
	}
	if p.MemoryMB < MinFrideInstanceMemoryMB || p.MemoryMB > MaxFrideInstanceMemoryMB {
		return fmt.Errorf("memory_mb %d is not between %d and %d", p.MemoryMB, MinFrideInstanceMemoryMB, MaxFrideInstanceMemoryMB)
	}
	if len(p.Repos) > maxFrideRepos || len(p.Identities) > maxInstanceIdentities || len(p.Egress) > maxInstanceEgress {
		return errors.New("too many repositories, identities or egress hosts")
	}
	for _, r := range p.Repos {
		if r == "" || len(r) > maxURLLen || !utf8.ValidString(r) || strings.ContainsAny(r, "\x00\r\n ") {
			return fmt.Errorf("repository %q", r)
		}
	}
	for _, id := range p.Identities {
		if id.ID == "" || len(id.ID) > 64 || id.Host == "" || len(id.Host) > maxHostLen || len(id.Token) > maxTokenLen ||
			strings.ContainsAny(id.ID+id.Host+id.Token, "\x00\r\n ") {
			return fmt.Errorf("identity %q", id.ID)
		}
	}
	for _, h := range p.Egress {
		if h == "" || len(h) > maxHostLen || strings.ContainsAny(h, "\x00\r\n /") {
			return fmt.Errorf("egress host %q", h)
		}
	}
	if p.Template != "" {
		return CheckTemplateRef(p.Template)
	}
	return nil
}
