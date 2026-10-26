package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/mcp/tools"
)

// The folders of a brain read_memory reads: its notes (only their
// markdown) and its skills.
const (
	memoriesDir = "memories"
	skillsDir   = "skills"
)

func describeReadMemory(c mcpCaller) mcp.Tool {
	brains := append([]string{c.run.Brain}, c.Granted(manifest.GrantRead)...)
	desc := "Read a brain's notes: a markdown file under memories/ or any file under skills/, or list such a folder (a path that is a folder, or none for memories/). "
	if len(brains) > 1 {
		desc += "You may read your own (" + c.run.Brain + ", the default) and those of: " + strings.Join(brains[1:], ", ") + ". What you learn about another brain goes into your own notes, never its files."
	} else {
		desc += "You may read only your own (" + c.run.Brain + ")."
	}
	return mcp.Tool{
		Description: desc,
		InputSchema: tools.Schema(map[string]any{
			"brain": map[string]any{"type": "string", "enum": brains, "description": "Whose notes; default your own."},
			"path":  tools.Str("Relative to the brain's folder, such as memories/people.md or skills/; default memories/."),
		}),
	}
}

func (b *App) callReadMemory(_ context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var args struct {
		Brain string `json:"brain"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ErrorResult(`the arguments are {"brain": "<id>", "path": "memories/…"}: ` + err.Error()), nil
	}
	id := args.Brain
	if id == "" {
		id = c.run.Brain
	}
	if !c.MayRead(id) {
		return mcp.ErrorResult(fmt.Sprintf("you may not read brain %q's notes", id)), nil
	}
	br := b.brainMgr.Get(id)
	if br == nil {
		return mcp.ErrorResult(fmt.Sprintf("brain %q is not running", id)), nil
	}
	rel, err := memoryPath(args.Path)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	text, err := readMemory(br.Dir(), rel)
	if err != nil {
		return mcp.ErrorResult(fmt.Sprintf("%s: %v", rel, err)), nil
	}
	return mcp.TextResult(text), nil
}

// memoryPath checks a path read_memory was given and returns it cleaned,
// slash-separated and relative to the brain's folder: memories/ or a
// folder or markdown file below it, or skills/ or anything below it.
func memoryPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		p = memoriesDir
	}
	clean := path.Clean(p)
	if path.IsAbs(clean) || !filepath.IsLocal(filepath.FromSlash(clean)) || strings.ContainsRune(p, '\\') {
		return "", fmt.Errorf("%q is not a path inside the brain's folder", p)
	}
	top, _, _ := strings.Cut(clean, "/")
	if top != memoriesDir && top != skillsDir {
		return "", fmt.Errorf("%q is outside memories/ and skills/, the only folders read_memory reads", p)
	}
	return clean, nil
}

// maxMemoryListing caps a folder listing's entries.
const maxMemoryListing = 500

// readMemory reads rel, which memoryPath let through, in the brain folder
// dir: a file whole (up to fsx.MaxBrainFile), a folder as a listing. The
// bridge reads it outside the sandbox while the brain controls the folder,
// so it follows no symlink on the way, not even one inside the folder,
// which could lead out of memories/ to the brain's history or its
// brain.json: each folder is opened from the one before, as it was found.
func readMemory(dir, rel string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	parts := strings.Split(rel, "/")
	cur := root
	defer func() { cur.Close() }()
	for i, part := range parts {
		st, err := cur.Lstat(part)
		if errors.Is(err, fs.ErrNotExist) {
			return "", errors.New("no such file or folder")
		}
		if err != nil {
			return "", err
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			return "", fsx.ErrSymlink
		}
		last := i == len(parts)-1
		if !st.IsDir() {
			if !last {
				return "", errors.New("no such file or folder")
			}
			if parts[0] == memoriesDir && !strings.HasSuffix(part, ".md") {
				return "", errors.New("only the markdown files (.md) under memories/ can be read")
			}
			data, err := fsx.ReadBrainFileRoot(cur, part)
			if err != nil {
				return "", err
			}
			return string(data), nil
		}
		next, err := cur.OpenRoot(part)
		if err != nil {
			return "", err
		}
		if now, err := next.Stat("."); err != nil || !os.SameFile(st, now) {
			next.Close()
			return "", errors.New("the folder changed while it was opened")
		}
		cur.Close()
		cur = next
	}
	return listMemory(cur, rel, parts[0] == memoriesDir)
}

// listMemory lists the folder root, which is rel: its folders and, under
// memories/, its markdown files, else all its files, one a line.
func listMemory(root *os.Root, rel string, markdownOnly bool) (string, error) {
	f, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer f.Close()
	entries, err := f.ReadDir(maxMemoryListing + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	var lines []string
	for _, e := range entries[:min(len(entries), maxMemoryListing)] {
		name := e.Name()
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			continue // never followed, so not offered
		case e.IsDir():
			lines = append(lines, rel+"/"+name+"/")
		case e.Type().IsRegular() && (!markdownOnly || strings.HasSuffix(name, ".md")):
			lines = append(lines, rel+"/"+name)
		}
	}
	slices.Sort(lines)
	if len(entries) > maxMemoryListing {
		lines = append(lines, fmt.Sprintf("(only the first %d entries)", maxMemoryListing))
	}
	if len(lines) == 0 {
		return rel + "/ is empty", nil
	}
	return strings.Join(lines, "\n"), nil
}
