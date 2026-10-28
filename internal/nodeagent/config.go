// Package nodeagent is `fridolin node`: the agent that runs on each
// machine of the fleet (docs/NODES.md). It holds the node's device key,
// connects out to the core, reconnects when the connection drops, and
// serves the capabilities the owner approved: allowlisted commands, files
// under granted roots, host facts and desktop notifications. It runs on
// macOS, Linux and Windows.
package nodeagent

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// The files in the node's state dir.
const (
	// ConfigFile is the node's settings, which its owner may edit: the
	// core's address, the name, and what the node offers. A change that
	// offers more needs the owner's approval again on the core.
	ConfigFile = "node.json"
	// KeyFile is the device's ed25519 key, hex; it never leaves the node.
	KeyFile = "node.key"
)

// Config is node.json.
type Config struct {
	// Core is the core's node port, host:port (ws://host:port and
	// wss://host:port work too).
	Core string `json:"core"`
	Name string `json:"name"`
	// Capabilities is what the node declares; the core approves it.
	// FrIDE is not among them: the agent declares it while FrIDE answers.
	Capabilities nodeproto.Capabilities `json:"capabilities"`
	// Fride is FrIDE on this machine, paired with `fridolin node fride
	// pair`; nil for none.
	Fride *FrideConfig `json:"fride,omitempty"`
	// AndroidBuild false keeps the node from offering android-build where
	// it finds a JDK and the Android SDK; unset or true offers it then.
	AndroidBuild *bool `json:"android_build,omitempty"`
	// Update false keeps the node from offering update, so the core never
	// replaces its fridolin; unset or true offers it (update.go).
	Update *bool `json:"update,omitempty"`
	// Box is set for a node in a box (box.go); nil for a native node.
	Box *BoxConfig `json:"box,omitempty"`
	// Service names the service of a second node on this machine:
	// fridolin-node-<service>, ai.fridolin.node.<service> on macOS, run
	// from the state dir's own fridolin (OwnBinary). Empty: the machine's
	// node, fridolin-node.
	Service string `json:"service,omitempty"`
	// FrideHost tunes fride_host, which the agent declares once `fridolin
	// node fride host setup` ran (fride_instances.go); nil for defaults.
	FrideHost *FrideHostConfig `json:"fride_host,omitempty"`
}

// DefaultCapabilities is what a node offers when its owner says nothing
// else: the OS's report-only commands, host facts and notifications, and
// no files.
func DefaultCapabilities(goos string) nodeproto.Capabilities {
	return nodeproto.Capabilities{
		Exec:   &nodeproto.ExecCap{Allow: nodeproto.DefaultExecAllow(goos)},
		Host:   true,
		Notify: true,
	}
}

// Check reports what is wrong with the config.
func (c Config) Check() error {
	if strings.TrimSpace(c.Core) == "" {
		return errors.New("core: the core's address is missing (host:port)")
	}
	if err := nodeproto.CheckName(c.Name); err != nil {
		return err
	}
	if c.Capabilities.Fride != nil {
		return errors.New("capabilities.fride: the agent declares FrIDE while it answers; pair it with fridolin node fride pair instead")
	}
	if c.Capabilities.AndroidBuild != nil {
		return errors.New(`capabilities.android_build: the agent declares it while it finds a JDK and the Android SDK; "android_build": false turns it off`)
	}
	if c.Capabilities.CoreHost != nil {
		return errors.New("capabilities.core_host: core-host lives in core-host.json, which only this machine writes: fridolin node core-host on")
	}
	if c.Capabilities.FrideHost != nil {
		return errors.New(`capabilities.fride_host: the agent declares it once fridolin node fride host setup ran; node.json's "fride_host" section only tunes it (reserve_mb)`)
	}
	if c.Capabilities.Update != nil {
		return errors.New(`capabilities.update: the agent declares it; "update": false turns it off`)
	}
	if b := c.Capabilities.Backup; b != nil && len(b.Folders) > 0 {
		return errors.New("capabilities.backup.folders: the owner picks the backup folders on the core; node.json names only the roots it lends (capabilities.backup.roots)")
	}
	if err := c.Capabilities.Check(); err != nil {
		return err
	}
	if err := c.Fride.Check(); err != nil {
		return err
	}
	if err := c.Box.Check(); err != nil {
		return err
	}
	if err := c.FrideHost.Check(); err != nil {
		return err
	}
	if c.Service != "" && !serviceNameRe.MatchString(c.Service) {
		return fmt.Errorf("service: %q is not a service name (a-z, 0-9 and -, at most 32)", c.Service)
	}
	if b := c.Capabilities.Backup; b != nil {
		for _, r := range b.Roots {
			if !filepath.IsAbs(r) {
				return fmt.Errorf("backup: root %q is not an absolute path on this system", r)
			}
		}
	}
	for kind, w := range map[string]*nodeproto.WorkstationCap{"agent": c.Capabilities.Agent, "terminal": c.Capabilities.Terminal} {
		if w == nil {
			continue
		}
		if len(w.Providers) > 0 || w.Sandbox != "" {
			return fmt.Errorf("capabilities.%s: providers and sandbox are what the agent finds; node.json names only roots and allow_home", kind)
		}
		for _, r := range w.Roots {
			if err := CheckWorkRoot(r, w.AllowHome, ""); err != nil {
				return fmt.Errorf("%s: root %w", kind, err)
			}
		}
	}
	if e := c.Capabilities.Exec; e != nil {
		for _, f := range e.AcceptFromCore {
			if err := CheckWorkRoot(f, false, ""); err != nil {
				return fmt.Errorf("exec: accept_from_core folder %w", err)
			}
		}
		if slices.ContainsFunc(e.Allow, func(r nodeproto.ExecRule) bool { return r.Core }) {
			return errors.New("exec.allow: a rule marked core is one the core added (exec-from-core.json); node.json's rules are the owner's")
		}
	}
	if fs := c.Capabilities.FS; fs != nil {
		for _, r := range fs.Roots {
			if !filepath.IsAbs(r.Path) {
				return fmt.Errorf("fs: root %q is not an absolute path on this system", r.Path)
			}
		}
	}
	return nil
}

// LoadConfig reads dir's node.json.
func LoadConfig(dir string) (Config, error) {
	var c Config
	data, err := os.ReadFile(filepath.Join(dir, ConfigFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, fmt.Errorf("%s has no %s: join a core first (fridolin node join <code> --core host:port)", dir, ConfigFile)
		}
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("reading %s: %w", filepath.Join(dir, ConfigFile), err)
	}
	if err := c.Check(); err != nil {
		return c, fmt.Errorf("%s: %w", filepath.Join(dir, ConfigFile), err)
	}
	return c, nil
}

// SaveConfig writes dir's node.json.
func SaveConfig(dir string, c Config) error {
	if err := c.Check(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, ConfigFile), append(data, '\n'), 0o600)
}

// LoadKey reads dir's device key.
func LoadKey(dir string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(filepath.Join(dir, KeyFile))
	if err != nil {
		return nil, err
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s is not a device key", filepath.Join(dir, KeyFile))
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// NewKey makes a device key and writes it to dir, replacing one there: a
// node that joins again is a new device.
func NewKey(dir string) (ed25519.PrivateKey, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(dir, KeyFile), []byte(hex.EncodeToString(key.Seed())+"\n"), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// DeviceID is the ID of key's device.
func DeviceID(key ed25519.PrivateKey) string {
	return nodeproto.DeviceIDFor(key.Public().(ed25519.PublicKey))
}

// writeFile writes data to path through a temp file and a rename, in a
// folder only its owner may enter.
func writeFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
