package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Icewater1337/fridolin/internal/doctor"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// brainRuntimeCheck says, for each brain set to Docker Sandboxes, where
// its next run happens and what its fallback would do. Brains on
// bubblewrap, the default, leave it out.
var brainRuntimeCheck = doctor.Check{ID: "brain-runtime", Title: "where brains set to Docker Sandboxes run", Applies: dockerBrainsSet, Run: checkBrainRuntime}

// dockerBrains are the approved brains whose runtime is docker-sandbox,
// by id, with their fallback.
func dockerBrains(env *doctor.Env) map[string]string {
	out := map[string]string{}
	if env.Config == nil {
		return out
	}
	for _, ref := range env.Config.BrainDirs {
		if m := ref.Approved; m != nil && m.Sandbox.RuntimeOf(env.Config.Sandbox.Runtime) == manifest.RuntimeDockerSandbox {
			out[m.ID] = m.Sandbox.FallbackOf()
		}
	}
	return out
}

func dockerBrainsSet(env *doctor.Env) bool { return len(dockerBrains(env)) > 0 }

// checkBrainRuntime asks the running bridge where each brain set to
// Docker Sandboxes runs now. It warns when any falls back or is refused,
// and when the bridge does not answer.
func checkBrainRuntime(ctx context.Context, env *doctor.Env) doctor.Result {
	want := dockerBrains(env)
	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var live []httpapi.BrainView
	var err error
	if env.LiveBrains == nil {
		err = fmt.Errorf("not asked")
	} else if data, e := env.LiveBrains(ctx, env.Config); e != nil {
		err = e
	} else {
		err = json.Unmarshal(data, &live)
	}
	if err != nil {
		return doctor.Result{Status: doctor.Warn, Detail: fmt.Sprintf("set to Docker Sandboxes: %s; the running bridge did not say where they run (%v)", strings.Join(ids, ", "), err),
			Fix: "start the bridge, then run doctor again"}
	}
	status, lines := doctor.OK, []string{}
	for _, id := range ids {
		var sb *httpapi.BrainSandbox
		for _, v := range live {
			if v.ID == id {
				sb = v.Sandbox
			}
		}
		switch {
		case sb == nil:
			status = doctor.Warn
			lines = append(lines, id+": not running")
		case sb.Effective == manifest.RuntimeDockerSandbox:
			lines = append(lines, fmt.Sprintf("%s: in a Docker Sandbox on %s", id, sb.Node))
		default:
			status = doctor.Warn
			lines = append(lines, fmt.Sprintf("%s: %s, as its fallback is %s, because %s", id, sb.Effective, sb.Fallback, sb.Reason))
		}
	}
	res := doctor.Result{Status: status, Detail: strings.Join(lines, "; ")}
	if status != doctor.OK {
		res.Fix = "fridolin doctor --node on the node, and docs/SANDBOXES.md"
	}
	return res
}
