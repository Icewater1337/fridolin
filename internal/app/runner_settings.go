package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"strings"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/sandbox"
)

// withArgsBeforePrint puts extra before the trailing -p that claudeArgs
// ends with, which ends an option list such as --mcp-config's.
func withArgsBeforePrint(args, extra []string) []string {
	if len(extra) == 0 || len(args) < 1 {
		return args
	}
	out := append([]string{}, args[:len(args)-1]...)
	out = append(out, extra...)
	return append(out, args[len(args)-1:]...)
}

// excludeInstructionsAbove returns the --settings for a brain in dir that
// keep the CLI from loading your instructions above the folder, which
// --setting-sources does not cover: the CLI reads the CLAUDE.md files and
// .claude rules of every folder up to / as part of the project, and your
// user memory in ~/.claude whatever the sources. Its claudeMdExcludes
// setting skips them, and it merges with the brain's own. It does not
// cover the skills, agents and commands in those .claude folders, nor
// stop a brain from reading the files with its tools; that takes the
// sandbox, which denies them all.
func excludeInstructionsAbove(dir string) string {
	data, err := json.Marshal(map[string][]string{"claudeMdExcludes": instructionsAbove(dir, HomeDir())})
	if err != nil {
		return ""
	}
	return string(data)
}

// instructionsAbove lists the claudeMdExcludes patterns for a brain in dir:
// the CLAUDE.md, CLAUDE.local.md, .claude/CLAUDE.md and .claude/rules of
// every folder above dir, and those in home's .claude, the user memory,
// for a brain outside home too. The patterns match paths as the CLI spells
// them, which differ below a symlink (a brains folder linked from another
// volume), so the folders above dir come along its path as configured and
// along its real path, and home both ways too.
func instructionsAbove(dir, home string) []string {
	var folders []string
	seen := map[string]bool{}
	for _, start := range []string{AbsPath(dir), fsx.Resolve(AbsPath(dir))} {
		for d := filepath.Dir(filepath.Clean(start)); ; d = filepath.Dir(d) {
			if !seen[d] {
				seen[d] = true
				folders = append(folders, d)
			}
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	var out []string
	patterns := func(d string, names ...string) {
		for _, name := range names {
			out = append(out, filepath.Join(globEscape(d), name))
		}
	}
	inDotClaude := []string{filepath.Join(".claude", "CLAUDE.md"), filepath.Join(".claude", "rules", "**")}
	for _, d := range folders {
		patterns(d, sandbox.InstructionFiles()...)
		patterns(d, inDotClaude...)
	}
	if filepath.IsAbs(home) {
		for _, h := range []string{home, fsx.Resolve(home)} {
			if !seen[h] {
				seen[h] = true
				patterns(h, inDotClaude...)
			}
		}
	}
	return out
}

// globEscape makes p a picomatch pattern, which the CLI matches
// claudeMdExcludes with, that matches the path p alone. A backslash does
// not do it, since the CLI turns every one into a / first, so each glob
// character goes in brackets: a folder named "notes (old)" would otherwise
// be a pattern for "notes old".
func globEscape(p string) string {
	var b strings.Builder
	for _, r := range p {
		if strings.ContainsRune("*?[]{}()!+@|", r) {
			b.WriteByte('[')
			b.WriteRune(r)
			b.WriteByte(']')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// mcpConfigOf returns what to pass with --mcp-config for the .mcp.json in
// the brain folder dir: the file, or its JSON without the servers the
// folder's settings turn off (see disabledMCPServers), or "" when there is
// nothing to pass. --strict-mcp-config makes the CLI ignore the file
// otherwise. The CLI refuses to start with an --mcp-config file that is not
// a JSON object holding an "mcpServers" object, which it never did for a
// project .mcp.json: `{}` or a misspelt key stops it as surely as a typo.
// Such a file is left out, as one mistake, or a file a manipulated brain
// wrote, would otherwise stop every run of the brain. Broken entries inside
// mcpServers it takes in its stride.
func mcpConfigOf(brainID, dir string) string {
	p := filepath.Join(dir, ".mcp.json")
	data, err := fsx.ReadBrainFile(dir, ".mcp.json")
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	var servers map[string]json.RawMessage
	if err == nil {
		servers, err = mcpServersOf(data)
	}
	var disabled map[string]bool
	if err == nil {
		disabled, err = disabledMCPServers(dir)
	}
	if err != nil {
		log.Printf("brain %s: leaving out %s: %v", brainID, p, err)
		return ""
	}
	kept := make(map[string]json.RawMessage, len(servers))
	for name, server := range servers {
		if !disabled[name] {
			kept[name] = server
		}
	}
	switch {
	case len(kept) == len(servers):
		return p
	case len(kept) == 0:
		return ""
	}
	// --mcp-config takes the JSON itself as well as a file.
	inline, err := json.Marshal(map[string]any{"mcpServers": kept})
	if err != nil {
		log.Printf("brain %s: leaving out %s: %v", brainID, p, err)
		return ""
	}
	return string(inline)
}

// disabledMCPServers returns the .mcp.json servers that the
// disabledMcpjsonServers of the brain folder dir's .claude/settings.json or
// settings.local.json turn off, where Claude Code records a project's
// server you declined. The CLI honours that list for a project's .mcp.json
// but not for --mcp-config, which would start a server you turned off on
// every run. Either file turns a server off, as it does for the CLI, and
// enabledMcpjsonServers keeps no other server from starting there
// (checked against CLI 2.1.302). When a file cannot be read, nothing can
// tell which servers it turns off, so the brain gets none.
func disabledMCPServers(dir string) (map[string]bool, error) {
	disabled := map[string]bool{}
	for _, name := range []string{"settings.json", "settings.local.json"} {
		p := filepath.Join(dir, ".claude", name)
		data, err := fsx.ReadBrainFile(dir, filepath.Join(".claude", name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		var settings map[string]json.RawMessage
		if err == nil {
			settings, err = jsonObject(data)
		}
		var names []string
		if raw, ok := settings["disabledMcpjsonServers"]; err == nil && ok {
			err = json.Unmarshal(raw, &names)
		}
		if err != nil {
			return nil, fmt.Errorf("cannot tell which servers %s turns off: %w", p, err)
		}
		for _, n := range names {
			disabled[n] = true
		}
	}
	return disabled, nil
}

// mcpServersOf returns the servers in the .mcp.json data by name, or why
// the CLI would refuse the file: it must be a JSON object whose
// "mcpServers", spelt exactly so, is an object.
func mcpServersOf(data []byte) (map[string]json.RawMessage, error) {
	top, err := jsonObject(data)
	if err != nil {
		return nil, err
	}
	var servers map[string]json.RawMessage
	if raw, ok := top["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return nil, fmt.Errorf("mcpServers is not an object: %w", err)
		}
	}
	if servers == nil {
		return nil, errors.New(`it has no "mcpServers" object`)
	}
	return servers, nil
}

// jsonObject decodes data, a JSON object, by key. Unlike a struct, a map
// matches keys exactly, as the CLI does, and it skips a leading byte order
// mark, as the CLI does too.
func jsonObject(data []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\uFEFF")), &obj); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	if obj == nil {
		return nil, errors.New("not a JSON object: null")
	}
	return obj, nil
}
