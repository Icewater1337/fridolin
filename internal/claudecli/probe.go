package claudecli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TestedClaudeVersion is the oldest Claude Code release the bridge was
// tested with. Brains rely on its flags (--effort, --setting-sources,
// --strict-mcp-config, --disallowed-tools), on the claudeMdExcludes
// setting passed with --settings, and on the shape of its stream-json
// events; brain.go notes what was checked against it. The CLI updates
// itself, so an older one usually means auto-updates are off.
const TestedClaudeVersion = "2.1.302"

// FailedProbeRetry is how long a `--version` run that failed (it timed
// out on a cold start, or exited non-zero) is remembered before Info runs
// the CLI again. Only a version found stays until the binary changes.
const FailedProbeRetry = time.Minute

type claudeCacheEntry struct {
	file string // the binary, symlinks resolved
	mod  time.Time
	size int64
	st   Status
	// retry is when a failed probe may run again; zero once a version was
	// found.
	retry time.Time
}

type Prober struct {
	mu    sync.Mutex
	cache map[string]claudeCacheEntry
	now   func() time.Time // time.Now when nil; tests set it
}

// ParseClaudeVersion reads the release from `claude --version` output such
// as "2.1.302 (Claude Code)".
func ParseClaudeVersion(s string) ([3]int, bool) {
	var v [3]int
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return v, false
	}
	num := strings.TrimPrefix(fields[0], "v")
	if i := strings.IndexAny(num, "-+"); i >= 0 {
		num = num[:i]
	}
	parts := strings.Split(num, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// VersionWarning says when the CLI is older than TestedClaudeVersion,
// or "" when it is not, or when its version cannot be told.
func VersionWarning(st Status) string {
	have, ok := ParseClaudeVersion(st.Version)
	if !st.Found || !ok {
		return ""
	}
	tested, _ := ParseClaudeVersion(TestedClaudeVersion)
	for i := range have {
		if have[i] != tested[i] {
			if have[i] > tested[i] {
				return ""
			}
			return fmt.Sprintf("The Claude Code CLI at %s is version %d.%d.%d, older than %s, the oldest release Fridolin was tested with: brains may fail, or run without some of their settings. Update it (claude update).",
				st.Path, have[0], have[1], have[2], TestedClaudeVersion)
		}
	}
	return ""
}

// Info runs `resolved --version` and caches what it found per path: a
// version until the binary changes, a failed run for FailedProbeRetry.
func (probe *Prober) Info(path, resolved string, env []string) Status {
	file, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		file = resolved
	}
	var mod time.Time
	var size int64
	if fi, err := os.Stat(file); err == nil {
		mod, size = fi.ModTime(), fi.Size()
	}

	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.cache == nil {
		probe.cache = make(map[string]claudeCacheEntry)
	}
	now := time.Now
	if probe.now != nil {
		now = probe.now
	}
	if c, ok := probe.cache[path]; ok && c.file == file && c.mod.Equal(mod) && c.size == size &&
		(c.retry.IsZero() || now().Before(c.retry)) {
		return c.st
	}
	st := Status{Path: resolved, Found: true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	cmd := exec.CommandContext(ctx, resolved, "--version")
	cmd.Env = env
	out, err := cmd.Output()
	cancel()
	entry := claudeCacheEntry{file: file, mod: mod, size: size}
	if v := strings.TrimSpace(string(out)); err == nil && v != "" {
		st.Version = v
	} else {
		// A failed run (a timeout on a cold start, a bad exit) must not
		// stick: the version, and with it VersionWarning, would be gone
		// until the binary changes.
		entry.retry = now().Add(FailedProbeRetry)
	}
	entry.st = st
	probe.cache[path] = entry
	return st
}
