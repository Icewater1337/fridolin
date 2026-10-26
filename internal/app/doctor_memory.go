package app

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/doctor"
)

// memoryDoctorCheck is M7's gate, for the brains that use the memory
// index.
var memoryDoctorCheck = doctor.Check{ID: "memory-index", Title: "memory index and its provenance gate", Required: true, Applies: memoryEnabled, Run: checkMemoryIndex}

// memoryBrains are the brains whose approved brain.json uses the memory
// index (memory.inject or memory.consolidate), by id, with their folders.
func memoryBrains(env *doctor.Env) map[string]string {
	out := map[string]string{}
	if env.Config == nil {
		return out
	}
	for _, ref := range env.Config.BrainDirs {
		if ref.Approved == nil {
			continue
		}
		if usesMemoryIndex(*ref.Approved) {
			out[ref.Approved.ID] = ref.Dir(env.Home)
		}
	}
	return out
}

func memoryEnabled(env *doctor.Env) bool { return len(memoryBrains(env)) > 0 }

// checkMemoryIndex is M7's gate: the provenance rule holds on this build
// (an untrusted fact is never recalled, the owner's is, with its anchor),
// and each brain that uses the index has one that holds its files as they
// are. The bridge syncs it before every run that recalls and every 30
// seconds, so a stale one means the bridge is not running or cannot read
// the folder.
func checkMemoryIndex(ctx context.Context, env *doctor.Env) doctor.Result {
	if env.MemorySelfTest != nil {
		if err := env.MemorySelfTest(ctx); err != nil {
			return doctor.Result{Status: doctor.Fail, Detail: "the provenance gate failed on this build: " + err.Error(), Fix: "report it: untrusted notes could reach a prompt; turn memory.inject off meanwhile"}
		}
	}
	brains := memoryBrains(env)
	ids := slices.Sorted(maps.Keys(brains))
	var notes []string
	status, fix := doctor.OK, ""
	for _, id := range ids {
		b := memoryBrain(env.Config.Layout(), env.Config, id, brains[id])
		stale, err := env.MemoryStale(ctx, b)
		switch {
		case err != nil:
			status, fix = doctor.Warn, "fridolin memory reindex --brain "+id
			notes = append(notes, id+": no index yet")
		case len(stale) > 0:
			status, fix = doctor.Warn, "fridolin memory reindex --brain "+id+" (or start the bridge, which keeps it up to date)"
			notes = append(notes, fmt.Sprintf("%s: %d files changed since it was indexed", id, len(stale)))
		default:
			notes = append(notes, id+": up to date")
		}
	}
	return doctor.Result{Status: status, Detail: "gate ok; " + strings.Join(notes, "; "), Fix: fix}
}
