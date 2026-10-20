package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/memindex"
)

// MemoryUsage is fridolin memory's help, outside a brain's run.
const MemoryUsage = `Usage:
  fridolin memory reindex [--brain ID]         build the memory index of every brain, or of one,
                                              afresh from its folder
  fridolin memory status [--brain ID]          what each index does not hold yet
  fridolin memory search --brain ID [--limit N] [--text] QUERY
                                              search a brain's index, every provenance, labelled

Inside a brain's run, fridolin memory search QUERY searches that brain's own
index through the bridge.

Flags:
  --config PATH   config file (default: $FRIDOLIN_CONFIG, else ~/.fridolin/config.json)
`

// MemoryCommand is fridolin memory outside a brain's run: it reads the
// config, finds the brains and works on their indexes in the data dir.
// It returns the exit status.
func MemoryCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, MemoryUsage)
		return boolExit(len(args) == 0)
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("fridolin memory "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file")
	brainID := fs.String("brain", "", "the brain's id")
	limit := fs.Int("limit", 10, "the most hits")
	text := fs.Bool("text", false, "one line per hit instead of JSON")
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fmt.Fprint(stdout, MemoryUsage)
				return 0
			}
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		positional, args = append(positional, fs.Arg(0)), fs.Args()[1:]
	}
	path, _ := ConfigPathFromPaths(*cfgPath, ConfigOptions().Paths)
	cfg, err := config.LoadExisting(path, ConfigOptions())
	if err != nil {
		fmt.Fprintf(stderr, "fridolin memory: %v\n", err)
		return 1
	}
	specs, _ := ResolveBrains(cfg)
	var brains []manifest.Spec
	for _, sp := range specs {
		if *brainID == "" || sp.Manifest.ID == *brainID {
			brains = append(brains, sp)
		}
	}
	if *brainID != "" && len(brains) == 0 {
		fmt.Fprintf(stderr, "fridolin memory: no brain %q that runs (fridolin check lists them)\n", *brainID)
		return 1
	}
	b := func(sp manifest.Spec) memindex.Brain {
		return memoryBrain(cfg.Layout(), cfg, sp.Manifest.ID, sp.Dir)
	}
	ctx := context.Background()
	switch sub {
	case "reindex", "status":
		if len(positional) > 0 {
			fmt.Fprintf(stderr, "fridolin memory %s takes no arguments, got %q\n", sub, positional)
			return 2
		}
		failed := false
		for _, sp := range brains {
			if sub == "status" {
				stale, err := memindex.Stale(ctx, b(sp))
				switch {
				case err != nil:
					fmt.Fprintf(stdout, "%s: no index yet (%v)\n", sp.Manifest.ID, err)
				case len(stale) == 0:
					fmt.Fprintf(stdout, "%s: up to date\n", sp.Manifest.ID)
				default:
					fmt.Fprintf(stdout, "%s: %d sources not indexed as they are: %s\n", sp.Manifest.ID, len(stale), strings.Join(stale, ", "))
				}
				continue
			}
			st, err := memindex.Reindex(ctx, b(sp))
			if err != nil {
				failed = true
				fmt.Fprintf(stderr, "%s: %v\n", sp.Manifest.ID, err)
				continue
			}
			fmt.Fprintf(stdout, "%s: %d files, %d chunks\n", sp.Manifest.ID, st.Files, st.Chunks)
		}
		return boolExit(failed)
	case "search":
		if *brainID == "" || len(positional) != 1 {
			fmt.Fprint(stderr, "fridolin memory search: give --brain ID and one QUERY (quote it when it has spaces)\n")
			return 2
		}
		hits, err := memindex.Search(ctx, b(brains[0]), positional[0], memindex.SearchOptions{Limit: *limit})
		if err != nil {
			fmt.Fprintf(stderr, "fridolin memory search: %v\n", err)
			return 1
		}
		return WriteMemoryHits(stdout, hits, *text)
	}
	fmt.Fprintf(stderr, "fridolin memory: unknown command %q\n\n%s", sub, MemoryUsage)
	return 2
}

func boolExit(failed bool) int {
	if failed {
		return 1
	}
	return 0
}

// WriteMemoryHits prints search hits as JSON, {"hits", "count"}, or with
// text one line each, provenance first.
func WriteMemoryHits(w io.Writer, hits []memindex.Hit, text bool) int {
	if text {
		for _, h := range hits {
			fmt.Fprintf(w, "[%s] %s: %s\n", h.Provenance, h.Ref(), strings.Join(strings.Fields(h.Text), " "))
		}
		return 0
	}
	if hits == nil {
		hits = []memindex.Hit{}
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{"hits": hits, "count": len(hits)}); err != nil {
		return 1
	}
	return 0
}
