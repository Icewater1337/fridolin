package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

// checkBrainDrift warns about the brains whose brain.json or scheduled
// agents on disk differ from the manifests the owner approved. The bridge
// runs the approved ones, and a core export takes the brain folders along
// by them; whoever reads brain.json for what stays home reads the wrong
// file until the change is approved (#189).
func checkBrainDrift(_ context.Context, env *Env) Result {
	if env.Config == nil {
		return NoConfig
	}
	var lines []string
	keepsHome := false
	for _, ref := range env.Config.BrainDirs {
		if ref.Approved == nil || ref.Approved.ID == "" {
			continue
		}
		d := manifest.DriftOf(ref.Dir(env.Home), *ref.Approved)
		if !d.Any() {
			continue
		}
		keepsHome = keepsHome || d.KeepsMoreHome()
		lines = append(lines, fmt.Sprintf("%s: %s", ref.Approved.ID, d.Describe()))
	}
	if len(lines) == 0 {
		return Result{Status: OK, Detail: "every brain folder is as you approved it"}
	}
	fix := "review and approve the changes in the dashboard (Brains); until then the bridge, the backups and a core export follow the approved manifests"
	if keepsHome {
		fix += ", and core export refuses while a brain.json on disk keeps more home than the approved one: approve it first, or pass --use-approved"
	}
	return Result{Status: Warn, Detail: strings.Join(lines, "; "), Fix: fix}
}
