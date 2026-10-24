package nodeagent

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/Icewater1337/fridolin/internal/platform"
)

// machineBound is the source's fixed scan for what a core that moves away
// leaves behind (#187 §4): report only, files read, nothing run. A box
// has nothing of its host.
func (a *Agent) machineBound() []string {
	if a.CoreScan != nil {
		return a.CoreScan()
	}
	if a.Config.Box != nil {
		return nil
	}
	home := platform.ExpandHome("~")
	if home == "~" {
		return nil
	}
	// Where fridolin core keeps its compose file (os.UserConfigDir).
	cfgDir := filepath.Join(home, ".config")
	if runtime.GOOS == "darwin" {
		cfgDir = filepath.Join(home, "Library", "Application Support")
	}
	return scanMachineBound(runtime.GOOS, home, cfgDir)
}

// scanMachineBound looks in home and the config dir of a goos machine.
func scanMachineBound(goos, home, cfgDir string) []string {
	var out []string
	if cfgDir != "" {
		if _, err := os.Stat(filepath.Join(cfgDir, "fridolin", "compose.host.yaml")); err == nil {
			out = append(out, "compose.host.yaml binds folders of this machine into the core (a moved native install's brain folders, sandbox.credentials_dir): they are missing on the target")
		}
	}
	var jobs []string
	switch goos {
	case "darwin":
		jobs, _ = filepath.Glob(filepath.Join(home, "Library", "LaunchAgents", "*.plist"))
	case "linux":
		jobs, _ = filepath.Glob(filepath.Join(home, ".config", "systemd", "user", "*.service"))
	}
	var tunnel, callers []string
	for _, j := range jobs {
		data, err := os.ReadFile(j)
		if err != nil || len(data) > 1<<20 {
			continue
		}
		s := string(data)
		name := filepath.Base(j)
		if strings.Contains(s, "cloudflared") {
			tunnel = append(tunnel, name)
		}
		if strings.Contains(s, "127.0.0.1:8765") || strings.Contains(s, "localhost:8765") || strings.Contains(s, "scripts.token") {
			callers = append(callers, name)
		}
	}
	sort.Strings(tunnel)
	sort.Strings(callers)
	if len(tunnel) > 0 {
		out = append(out, "a Cloudflare tunnel runs on this machine ("+strings.Join(tunnel, ", ")+"): the site goes offline; run the tunnel in the container (Site, Run the tunnel) before moving")
	}
	if len(callers) > 0 {
		out = append(out, "host jobs call the core at 127.0.0.1:8765 ("+strings.Join(callers, ", ")+"): they stop reaching it; make them scheduled agents, or point them at the core's tailnet address")
	}
	if goos == "darwin" {
		out = append(out, "Apple-only work (Xcode and iOS builds, osascript, launchctl) stays on this Mac: keep a native node here for it")
	}
	return out
}
