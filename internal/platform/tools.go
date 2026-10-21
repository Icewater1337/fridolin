package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// toolDirs are where package managers put programs that a service's PATH
// (launchd's /usr/bin:/bin:/usr/sbin:/sbin) may leave out.
func toolDirs() []string { return []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"} }

// LookTool finds the program name as an absolute path: in the PATH, else,
// outside Windows, in /opt/homebrew/bin, /usr/local/bin, /usr/bin or /bin.
// A program found relative to the working directory does not count.
func LookTool(name string) (string, error) {
	return lookTool(name, exec.LookPath, runtime.GOOS)
}

func lookTool(name string, look func(string) (string, error), goos string) (string, error) {
	if name == "" || strings.ContainsAny(name, `/\`) {
		return "", errors.New("a program's bare name is wanted")
	}
	p, err := look(name)
	if err == nil && filepath.IsAbs(p) {
		return p, nil
	}
	if goos != "windows" {
		for _, d := range toolDirs() {
			c := filepath.Join(d, name)
			if st, serr := os.Stat(c); serr == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
				return c, nil
			}
		}
	}
	if err == nil {
		err = errors.New("found only relative to the working directory")
	}
	return "", err
}

// EnvWith is the process's environment with each of set ("NAME=value")
// replacing a variable of that name.
func EnvWith(set ...string) []string {
	return envWith(os.Environ(), set...)
}

func envWith(env []string, set ...string) []string {
	out := make([]string, 0, len(env)+len(set))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.ContainsFunc(set, func(s string) bool { return strings.HasPrefix(s, name+"=") }) {
			out = append(out, kv)
		}
	}
	return append(out, set...)
}

// ExpandHome replaces a leading ~ (alone, or before a separator) with the
// home folder, and leaves any other path as it is.
func ExpandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p
	}
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return p
	}
	return filepath.Join(h, p[1:])
}
