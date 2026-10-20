package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/vault"
)

// The vault tools (docs/PLAN.md, M3): vault_ls, vault_get, vault_put and
// vault_search, each through the run's scope. A run's scope is its own
// brain's vault paths (vaultLayout.brainPrefixes) and, for a core run,
// those of the brains its brain.json grants `files`. A delegated run sees
// its own paths only.

// vaultPutMax bounds what vault_put takes as text.
const vaultPutMax = 1 << 20

// vaultScope is the caller's view of the vault: its own paths and, with
// granted, those of the brains it is granted files of. A files grant
// lets the core fetch files, not write them: vault_put asks for its own.
func (b *App) vaultScope(c mcpCaller, granted bool) (*vault.Scope, error) {
	v, _, err := b.vault.vault()
	if err != nil {
		return nil, err
	}
	l := b.vault.currentLayout()
	prefixes := l.brainPrefixes(c.run.Brain)
	if granted {
		for _, id := range c.granted(manifest.GrantFiles) {
			prefixes = append(prefixes, l.brainPrefixes(id)...)
		}
	}
	return v.Scoped(prefixes)
}

func (b *App) registerVaultTools(r *mcp.Registry[mcpCaller]) {
	available := func(mcpCaller) bool { return b.vault != nil && b.vault.Running() }
	r.Register(mcp.Entry[mcpCaller]{
		Name: "vault_ls", Available: available,
		Describe: func(c mcpCaller) mcp.Tool {
			return mcp.Tool{Description: "List a folder of the vault: its files, on disk (tier hot or both) or only in the encrypted cold tier (tier cold), and its subfolders. Paths are relative to the vault, such as " + b.vaultExample(c) + "; leave path empty to see the folders you may use.",
				InputSchema: schema(map[string]any{"path": strProp("The folder, relative to the vault; empty for the top.")})}
		},
		Call: b.callVaultLs,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name: "vault_get", Available: available,
		Describe: func(c mcpCaller) mcp.Tool {
			return mcp.Tool{Description: "Bring a vault file back from the cold tier (when it is cold) and get a path you can read it at. A file that would take the hot tier over its budget is refused: ask the person first, then call again with allow_over_budget.",
				InputSchema: schema(map[string]any{
					"path":              strProp("The file's vault path, as vault_ls or vault_search show it, or its absolute path (a message's file, or a .cold.json's folder plus the name)."),
					"allow_over_budget": map[string]any{"type": "boolean", "description": "Only after the person agreed: bring the file back even though the hot tier goes over its budget."},
				}, "path")}
		},
		Call: b.callVaultGet,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name: "vault_put", Available: available,
		Describe: func(c mcpCaller) mcp.Tool {
			return mcp.Tool{Description: "Store a file in the vault at a path of yours (such as " + b.vaultExample(c) + "/notes.md): give its text, or from, a file in your folder, your media inbox or your temp folder. It is kept on disk and, encrypted, in the cold tier.",
				InputSchema: schema(map[string]any{
					"path": strProp("Where in the vault, relative to it."),
					"text": strProp("The file's content, as text."),
					"from": strProp("A file to copy instead: an absolute path in your folder, your media inbox or your temp folder."),
				}, "path")}
		},
		Call: b.callVaultPut,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name: "vault_search", Available: available,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{Description: "Find vault files whose path contains every word of the query (not their contents), most recently used first, hot and cold alike.",
				InputSchema: schema(map[string]any{
					"query": strProp("Words of the path."),
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "At most this many (default 50)."},
				}, "query")}
		},
		Call: b.callVaultSearch,
	})
}

// vaultRel takes a path a run names, as a vault path or as the absolute
// path of a file in the hot root (a message's file, say), as a vault path.
func (b *App) vaultRel(p string) string {
	if filepath.IsAbs(p) {
		if rel, ok := b.vault.currentLayout().rel(filepath.Clean(p)); ok {
			return rel
		}
	}
	return strings.Trim(p, "/")
}

// vaultExample is one of the caller's own vault folders, for descriptions.
func (b *App) vaultExample(c mcpCaller) string {
	if b.vault == nil {
		return "vault/documents/" + c.run.Brain
	}
	return b.vault.currentLayout().brainPrefixes(c.run.Brain)[0]
}

// vaultToolError turns an error into what the model reads.
func vaultToolError(err error) *mcp.CallResult {
	switch {
	case errors.Is(err, vault.ErrOutOfScope):
		return mcp.ErrorResult(err.Error() + ": use vault_ls with an empty path to see the folders you may use")
	case errors.Is(err, vault.ErrNotFound):
		return mcp.ErrorResult(err.Error())
	case errors.Is(err, vault.ErrInsufficientSpace):
		return mcp.ErrorResult(err.Error() + ". Ask the person whether to bring it back anyway; if they agree, call vault_get again with allow_over_budget.")
	}
	return mcp.ErrorResult("the vault failed: " + err.Error())
}

func jsonResult(v any) *mcp.CallResult {
	data, _ := json.MarshalIndent(v, "", "  ")
	return mcp.TextResult(string(data))
}

func (b *App) callVaultLs(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ErrorResult(`the arguments are {"path": "<folder>"}: ` + err.Error()), nil
	}
	sc, err := b.vaultScope(c, true)
	if err != nil {
		return vaultToolError(err), nil
	}
	entries, err := sc.Ls(ctx, b.vaultRel(args.Path))
	if err != nil {
		return vaultToolError(err), nil
	}
	if entries == nil {
		entries = []vault.Entry{}
	}
	return jsonResult(map[string]any{"path": args.Path, "entries": entries}), nil
}

func (b *App) callVaultGet(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var args struct {
		Path            string `json:"path"`
		AllowOverBudget bool   `json:"allow_over_budget"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ErrorResult(`the arguments are {"path": "<file>"}: ` + err.Error()), nil
	}
	sc, err := b.vaultScope(c, true)
	if err != nil {
		return vaultToolError(err), nil
	}
	rel := b.vaultRel(args.Path)
	abs, err := sc.Get(ctx, rel, vault.GetOptions{AllowOverBudget: args.AllowOverBudget})
	if err != nil {
		return vaultToolError(err), nil
	}
	readable := abs
	if !insideAny(abs, b.readableTrees(c.run.Brain)) {
		// Outside what the run reads directly (vault/documents/ in the data
		// dir, a granted brain's inbox): a copy in its temp folder.
		if readable, err = readableCopy(b.cfgs.Boot().DataDir, c.run.Brain, rel, abs); err != nil {
			return vaultToolError(err), nil
		}
	}
	e, _ := sc.Stat(ctx, rel)
	return jsonResult(map[string]any{"path": readable, "vault_path": rel, "size": e.Size}), nil
}

func (b *App) callVaultPut(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var args struct {
		Path string  `json:"path"`
		Text *string `json:"text"`
		From string  `json:"from"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ErrorResult(`the arguments are {"path": "<file>", "text": "…"} or {"path": "<file>", "from": "<absolute path>"}: ` + err.Error()), nil
	}
	if (args.Text == nil) == (args.From == "") {
		return mcp.ErrorResult("give the file's text or from, one of them"), nil
	}
	sc, err := b.vaultScope(c, false)
	if err != nil {
		return vaultToolError(err), nil
	}
	var body io.Reader
	if args.Text != nil {
		if len(*args.Text) > vaultPutMax {
			return mcp.ErrorResult(fmt.Sprintf("text is more than %d bytes: write it to a file in your folder and pass it as from", vaultPutMax)), nil
		}
		body = strings.NewReader(*args.Text)
	} else {
		f, err := b.openRunFile(c.run.Brain, args.From)
		if err != nil {
			return mcp.ErrorResult(err.Error()), nil
		}
		defer f.Close()
		body = f
	}
	e, err := sc.Put(ctx, b.vaultRel(args.Path), body, vault.ProvenanceAgent)
	if err != nil {
		return vaultToolError(err), nil
	}
	return jsonResult(e), nil
}

// openRunFile opens from, a file brain id's run may read and hands to
// vault_put: one in its folder, its media inbox or its temp folder. It
// opens it through os.Root, so a symlink cannot lead out of them: the
// bridge, which reads it, runs outside the sandbox.
func (b *App) openRunFile(id, from string) (*os.File, error) {
	if !filepath.IsAbs(from) {
		return nil, errors.New("from must be an absolute path")
	}
	from = filepath.Clean(from)
	dirs := append(b.readableTrees(id), brainStateOf(AbsPath(b.cfgs.Boot().DataDir), id).tmp)
	for _, d := range dirs {
		d = filepath.Clean(d)
		rel, err := filepath.Rel(d, from)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		root, err := os.OpenRoot(d)
		if err != nil {
			return nil, err
		}
		f, err := root.Open(rel)
		_ = root.Close()
		if err != nil {
			return nil, fmt.Errorf("from: %w", err)
		}
		if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
			f.Close()
			return nil, errors.New("from is not a regular file")
		}
		return f, nil
	}
	return nil, errors.New("from must be a file in your folder, your media inbox or your temp folder")
}

func (b *App) callVaultSearch(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ErrorResult(`the arguments are {"query": "…"}: ` + err.Error()), nil
	}
	sc, err := b.vaultScope(c, true)
	if err != nil {
		return vaultToolError(err), nil
	}
	found, err := sc.Search(ctx, args.Query, min(max(args.Limit, 0), 200))
	if err != nil {
		return vaultToolError(err), nil
	}
	if found == nil {
		found = []vault.Entry{}
	}
	return jsonResult(map[string]any{"query": args.Query, "files": found}), nil
}
