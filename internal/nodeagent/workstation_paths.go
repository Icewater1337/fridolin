package nodeagent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// The folders of the workstation family (agent, terminal) are confined
// here. A root is checked when node.json is loaded and again at every
// call; a call's folder must lie under an approved root as written, and
// its real path (symlinks resolved) under that root's real path, so that
// neither .. nor a symlink inside a root leads elsewhere. The session then
// starts in the real folder, inside the platform's sandbox where there is
// one (workstation_sandbox.go), which keeps its writes in that folder.

// homeDir is the user's home folder; a variable for the tests.
var homeDir = os.UserHomeDir

// deniedHome are home-relative folders no workstation root may be or lie
// in, and which a sandboxed session may not read: keys, credentials, the
// node's and the bridge's own state.
var deniedHome = []string{
	".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".config/gh", ".config/gcloud", ".config/rclone",
	".fridolin", ".whatsapp-claude-bridge", ".password-store",
	"Library/Application Support/fridolin-node", ".local/state/fridolin-node",
	".git-credentials", ".netrc",
}

// secretName is a hidden folder's name that says it holds secrets.
var secretName = regexp.MustCompile(`secret|credential|token|keyring|vault`)

// systemRoots are folders no workstation root may be or lie in.
func systemRoots() []string {
	if runtime.GOOS == "windows" {
		win := os.Getenv("SystemRoot")
		if win == "" {
			win = `C:\Windows`
		}
		out := []string{win}
		for _, v := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramData"} {
			if p := os.Getenv(v); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return []string{"/bin", "/sbin", "/usr", "/etc", "/System", "/Library", "/private/etc", "/dev", "/proc", "/sys", "/boot", "/opt/homebrew", "/Applications"}
}

// deniedPaths are the absolute places of deniedHome and the state dir.
func deniedPaths(stateDir string) []string {
	var out []string
	if home, err := homeDir(); err == nil && home != "" {
		for _, d := range deniedHome {
			out = append(out, filepath.Clean(filepath.Join(home, filepath.FromSlash(d))))
		}
		// And every hidden folder of the home folder named for secrets,
		// credentials or tokens.
		if ents, err := os.ReadDir(home); err == nil {
			for _, e := range ents {
				if n := strings.ToLower(e.Name()); strings.HasPrefix(n, ".") && secretName.MatchString(n) {
					out = append(out, filepath.Join(home, e.Name()))
				}
			}
		}
	}
	if stateDir != "" {
		out = append(out, filepath.Clean(stateDir))
	}
	return out
}

// isVolumeRoot reports whether p is / or a drive's root.
func isVolumeRoot(p string) bool {
	p = filepath.Clean(p)
	return p == filepath.Dir(p)
}

// CheckWorkRoot reports what is wrong with root as a workstation root on
// this machine: it must be an absolute folder; never a volume's root, a
// system folder or a place of deniedPaths, nor lie in one; and never the
// home folder or a folder above it, but for the home folder itself with
// allowHome.
func CheckWorkRoot(root string, allowHome bool, stateDir string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("%q is not an absolute path on this system", root)
	}
	forms := []string{filepath.Clean(root)}
	if real, err := realPath(root); err == nil && real != forms[0] {
		forms = append(forms, real)
	}
	home, _ := homeDir()
	for _, p := range forms {
		if isVolumeRoot(p) {
			return fmt.Errorf("%s is a whole drive; grant a folder", root)
		}
		for _, s := range append(systemRoots(), deniedPaths(stateDir)...) {
			if inside(s, p) || inside(p, s) && !(home != "" && inside(p, home)) {
				// A root may hold a denied place only by holding the home
				// folder, which needs allowHome below.
				return fmt.Errorf("%s is or holds a place a node never grants (%s)", root, s)
			}
		}
		if home != "" {
			homes := []string{filepath.Clean(home)}
			if real, err := realPath(home); err == nil && real != homes[0] {
				homes = append(homes, real)
			}
			for _, h := range homes {
				if inside(p, h) && !(allowHome && sameFile(p, h)) {
					if sameFile(p, h) {
						return fmt.Errorf("%s is the home folder; grant a folder in it, or the home folder itself with --allow-home", root)
					}
					return fmt.Errorf("%s holds the home folder; a node never grants that", root)
				}
			}
		}
	}
	return nil
}

func sameFile(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// workRoots is the capability of kind, "agent" or "terminal".
func workCap(eff nodeproto.Capabilities, kind string) *nodeproto.WorkstationCap {
	if kind == "agent" {
		return eff.Agent
	}
	return eff.Terminal
}

// confinedWorkFolder is the folder a call of kind names, real, after
// checking that it is a folder under an approved root, as written and as
// its symlinks resolve, and in no denied place.
func (a *Agent) confinedWorkFolder(eff nodeproto.Capabilities, kind, requested string) (string, *nodeproto.Error) {
	w := workCap(eff, kind)
	if w == nil {
		return "", nodeproto.Errorf(nodeproto.CodeDenied, "%s is not approved on this node", kind)
	}
	if requested == "" || !filepath.IsAbs(requested) || strings.ContainsRune(requested, 0) {
		return "", nodeproto.Errorf(nodeproto.CodeBadRequest, "folder %q is not absolute", requested)
	}
	clean := filepath.Clean(requested)
	i := slices.IndexFunc(w.Roots, func(r string) bool { return filepath.IsAbs(r) && inside(r, clean) })
	if i < 0 {
		return "", nodeproto.Errorf(nodeproto.CodeOutsideRoot, "%s is not under a folder granted for %s", clean, kind)
	}
	root := w.Roots[i]
	if err := CheckWorkRoot(root, w.AllowHome, a.Dir); err != nil {
		return "", nodeproto.Errorf(nodeproto.CodeDenied, "%v", err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fsError(err, root)
	}
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fsError(err, clean)
	}
	if !inside(realRoot, real) {
		return "", nodeproto.Errorf(nodeproto.CodeOutsideRoot, "%s leads out of %s", clean, root)
	}
	for _, d := range deniedPaths(a.Dir) {
		if inside(d, real) {
			return "", nodeproto.Errorf(nodeproto.CodeDenied, "%s lies in %s, which a node never grants", clean, d)
		}
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", fsError(err, clean)
	}
	if !st.IsDir() {
		return "", nodeproto.Errorf(nodeproto.CodeBadRequest, "%s is not a folder", clean)
	}
	return real, nil
}
