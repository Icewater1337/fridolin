package nodeagent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The sandboxed FrIDE's Docker disk and shared caches (docs/NODES.md,
// "Docker disk" and "Shared caches").
const (
	// DefaultFrideSandboxDockerDisk is the size of the sandbox's
	// /var/lib/docker, for compose stacks: sbx's default is 10g. The disk
	// is a sparse file on the host, so it takes only what is used.
	DefaultFrideSandboxDockerDisk = "40g"
	// frideSandboxDiskEnv sizes one new sandbox's Docker disk (sbx
	// v0.50.0 has no flag for it; the setting sandbox.disk.dockerVolume
	// would size every new sandbox).
	frideSandboxDiskEnv = "DOCKER_SANDBOXES_DOCKER_SIZE"
)

// dockerDiskRe is a size sbx takes: a number of MiB or GiB.
var dockerDiskRe = regexp.MustCompile(`^[1-9][0-9]{0,5}[mMgG]$`)

// checkDockerDisk is size, or the default for "", or why it is not one.
func checkDockerDisk(size string) (string, error) {
	if size == "" {
		return DefaultFrideSandboxDockerDisk, nil
	}
	if !dockerDiskRe.MatchString(size) {
		return "", fmt.Errorf("--docker-disk %q: a size like 40g or 20480m", size)
	}
	return strings.ToLower(size), nil
}

// frideSandboxCacheDirs are the shared caches' folders.
var frideSandboxCacheDirs = []string{"gomod", "gobuild", "npm"}

// prepareCaches makes the shared caches' folders in dir and returns dir
// cleaned, or "" for none. The folder is mounted read-write at its own
// path, so it must not hold the base folder or lie in it.
func prepareCaches(dir, base string) (string, error) {
	if dir == "" {
		return "", nil
	}
	for _, d := range frideSandboxCacheDirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			return "", err
		}
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	_, in := within(base, real)
	_, holds := within(real, base)
	if in || holds {
		return "", fmt.Errorf("the caches %s and the base folder %s must not hold one another", real, base)
	}
	return real, nil
}

// frideSandboxCacheEnv is what points Go and npm in the sandbox at the
// shared caches in dir: in every exec, so in FrIDE's environment and its
// agents' (FrIDE's CleanEnv keeps them). The module cache stays writable
// (-modcacherw), so that the host can delete it.
func frideSandboxCacheEnv(dir string) []string {
	if dir == "" {
		return nil
	}
	return []string{
		"GOMODCACHE=" + filepath.Join(dir, "gomod"),
		"GOCACHE=" + filepath.Join(dir, "gobuild"),
		"npm_config_cache=" + filepath.Join(dir, "npm"),
		"GOFLAGS=-modcacherw",
	}
}
