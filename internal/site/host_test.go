package site

import (
	"errors"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/google"
	"github.com/Icewater1337/fridolin/internal/sandbox"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// gitPath is git's full path: sandbox-exec runs a program by path.
func gitPath() string {
	if p, err := exec.LookPath("git"); err == nil {
		return p
	}
	return "/usr/bin/git"
}

// siteEnv is the whole environment of a site process: nothing of the bridge's
// own environment (tokens, API keys) reaches it.
func siteEnv(bin string, extra ...string) []string {
	env := []string{
		"HOME=" + homeDir(),
		"PATH=" + siteEnvPath(bin),
		"LANG=en_US.UTF-8",
	}
	for _, k := range []string{"USER", "LOGNAME", "TMPDIR"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return append(env, extra...)
}

// siteEnvPath is the PATH of a site process: the folder of the program it
// runs (node's, so npm and npx are found), then the usual ones.
func siteEnvPath(bin string) string {
	return filepath.Dir(bin) + ":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
}

// findNode finds Node.js (the site needs 22 or newer): on PATH, in the usual
// folders, or the newest nvm install. Symlinks are resolved, so npm is found
// next to it.
func findNode() (string, error) {
	bin, err := findTool("node")
	if err != nil {
		found, _ := filepath.Glob(filepath.Join(homeDir(), ".nvm/versions/node/v*/bin/node"))
		slices.SortFunc(found, func(a, b string) int { return compareVersionDirs(b, a) })
		if len(found) == 0 {
			return "", errors.New("node is not installed (macOS: brew install node); the site needs Node.js 22 or newer")
		}
		bin = found[0]
	}
	if real, err := filepath.EvalSymlinks(bin); err == nil {
		bin = real
	}
	return bin, nil
}

// compareVersionDirs orders …/v22.14.0/bin/node before …/v24.1.0/bin/node.
func compareVersionDirs(a, b string) int {
	va := strings.Split(strings.TrimPrefix(filepath.Base(filepath.Dir(filepath.Dir(a))), "v"), ".")
	vb := strings.Split(strings.TrimPrefix(filepath.Base(filepath.Dir(filepath.Dir(b))), "v"), ".")
	for i := 0; i < len(va) && i < len(vb); i++ {
		var x, y int
		fmt.Sscan(va[i], &x)
		fmt.Sscan(vb[i], &y)
		if x != y {
			return x - y
		}
	}
	return len(va) - len(vb)
}

// findTool looks on PATH and in the usual install folders (launchd's PATH is short).
func findTool(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, dir := range toolFallbackDirs() {
		if p := filepath.Join(dir, name); fsx.Exists(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found", name)
}

// toolFallbackDirs are where findTool looks for a program that is not on
// PATH, which under the service it often is not. The sandbox keeps them
// from brains like the PATH folders.
func toolFallbackDirs() []string {
	return []string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(homeDir(), ".local/bin")}
}

// NewSiteManager supplies the host capabilities before constructing the service.
func NewSiteManager(opts SiteOptions) *SiteManager {
	h := &opts.Host
	if h.Home == nil {
		h.Home = homeDir
	}
	if h.Node == nil {
		h.Node = findNode
	}
	if h.Tool == nil {
		h.Tool = findTool
	}
	if h.GitPath == nil {
		h.GitPath = gitPath
	}
	if h.Env == nil {
		h.Env = siteEnv
	}
	if h.Environ == nil {
		h.Environ = os.Environ
	}
	if h.Profile == nil {
		h.Profile = siteSandboxProfile
	}
	if h.GoogleClientType == nil {
		h.GoogleClientType = func(data []byte) (string, error) {
			c, err := google.ParseClient(data)
			if err != nil {
				return "", err
			}
			return c.Type, nil
		}
	}
	return New(opts)
}

func siteSandboxProfile(in sandbox.SitePolicy) (string, error) {
	in.WorkingDir, in.WorkingDirErr = os.Getwd()
	in.Logf = log.Printf
	return sandbox.SiteProfile(in)
}

func homeDir() string { home, _ := os.UserHomeDir(); return home }
