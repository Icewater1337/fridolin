package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// testedClaudeVersion is the oldest Claude Code release the bridge was
// tested with. Brains rely on its flags (--effort, --setting-sources,
// --strict-mcp-config, --disallowed-tools), on the claudeMdExcludes
// setting passed with --settings, and on the shape of its stream-json
// events; brain.go notes what was checked against it. The CLI updates
// itself, so an older one usually means auto-updates are off.
const testedClaudeVersion = "2.1.302"

type claudeCacheEntry struct {
	file string // the binary, symlinks resolved
	mod  time.Time
	size int64
	st   claudeStatus
}

var (
	claudeVersionMu    sync.Mutex
	claudeVersionCache = map[string]claudeCacheEntry{}
)

// claudeInfo finds the CLI and its version. The version is cached until the
// binary changes: `claude` on the PATH is a symlink the CLI moves to each
// new release it installs, and the dashboard must not show the old one for
// as long as the bridge runs. A CLI that is not found is looked up again
// every time, so installing it shows at once. It runs the CLI with a
// brain's environment: a claude_path wrapper may run what a brain can
// change.
func claudeInfo(path string, passthrough []string) claudeStatus {
	p, err := exec.LookPath(path)
	if err != nil {
		return claudeStatus{Path: path}
	}
	file, err := filepath.EvalSymlinks(p)
	if err != nil {
		file = p
	}
	var mod time.Time
	var size int64
	if fi, err := os.Stat(file); err == nil {
		mod, size = fi.ModTime(), fi.Size()
	}

	claudeVersionMu.Lock()
	defer claudeVersionMu.Unlock()
	if c, ok := claudeVersionCache[path]; ok && c.file == file && c.mod.Equal(mod) && c.size == size {
		return c.st
	}
	st := claudeStatus{Path: p, Found: true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	cmd := exec.CommandContext(ctx, p, "--version")
	cmd.Env = childEnv(claudeChild, passthrough)
	out, err := cmd.Output()
	cancel()
	if err == nil {
		st.Version = strings.TrimSpace(string(out))
	}
	claudeVersionCache[path] = claudeCacheEntry{file: file, mod: mod, size: size, st: st}
	return st
}

// parseClaudeVersion reads the release from `claude --version` output such
// as "2.1.302 (Claude Code)".
func parseClaudeVersion(s string) ([3]int, bool) {
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

// claudeVersionWarning says when the CLI is older than testedClaudeVersion,
// or "" when it is not, or when its version cannot be told.
func claudeVersionWarning(st claudeStatus) string {
	have, ok := parseClaudeVersion(st.Version)
	if !st.Found || !ok {
		return ""
	}
	tested, _ := parseClaudeVersion(testedClaudeVersion)
	for i := range have {
		if have[i] != tested[i] {
			if have[i] > tested[i] {
				return ""
			}
			return fmt.Sprintf("The Claude Code CLI at %s is version %d.%d.%d, older than %s, the oldest release Fridolin was tested with: brains may fail, or run without some of their settings. Update it (claude update).",
				st.Path, have[0], have[1], have[2], testedClaudeVersion)
		}
	}
	return ""
}

// logClaudeCLI says at startup which CLI brains run and warns when it is
// missing or too old, so a log excerpt shows it next to the bridge's own
// version.
func logClaudeCLI(path string, passthrough []string) {
	st := claudeInfo(path, passthrough)
	switch {
	case !st.Found:
		log.Printf("WARNING: the Claude Code CLI (%s) was not found, so brains cannot run. Set its path under Settings → Claude.", path)
	case st.Version == "":
		log.Printf("Claude Code CLI: %s (`--version` gave no answer)", st.Path)
	default:
		log.Printf("Claude Code CLI: %s, %s", st.Path, st.Version)
	}
	if w := claudeVersionWarning(st); w != "" {
		log.Printf("WARNING: %s", w)
	}
}
