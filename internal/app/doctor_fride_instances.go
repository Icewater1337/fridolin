package app

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/doctor"
	"github.com/Icewater1337/fridolin/internal/gitident"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/version"
)

// frideInstancesDoctorCheck checks each FrIDE instance fride.instances
// names (docs/NODES.md, "The core and FrIDE instances"): some approved
// node can run it, with room for its smallest start, and the core holds
// the git identities it names.
var frideInstancesDoctorCheck = doctor.Check{ID: "fride-instances", Title: "FrIDE instances can run", Applies: frideInstancesOn, Run: checkFrideInstances}

func frideInstancesOn(env *doctor.Env) bool {
	return nodesOn(env) && len(env.Config.Fride.Instances) > 0
}

// liveIdentity is a git identity as the running bridge has it: set, and
// why it cannot hand it out when it cannot (the GitHub app may only
// read code, say).
type liveIdentity struct {
	set  bool
	note string
}

// liveIdentities are the git identities the running bridge holds, by id;
// nil when it does not answer.
func liveIdentities(ctx context.Context, env *doctor.Env) map[string]liveIdentity {
	if env.LiveGet == nil {
		return nil
	}
	data, err := env.LiveGet(ctx, env.Config, "/api/git-identities")
	if err != nil {
		return nil
	}
	var view struct {
		Identities []struct {
			ID   string `json:"id"`
			Set  bool   `json:"set"`
			Note string `json:"note"`
		} `json:"identities"`
	}
	if json.Unmarshal(data, &view) != nil {
		return nil
	}
	out := map[string]liveIdentity{}
	for _, id := range view.Identities {
		out[id.ID] = liveIdentity{set: id.Set, note: id.Note}
	}
	return out
}

// frideInstanceProblems says what keeps instance name from running: no
// node for it, none with room for its smallest start, and identities the
// core does not hold (have, from the bridge; nil when it does not answer,
// and only GitLab's file is looked at then).
func frideInstanceProblems(env *doctor.Env, name string, ic config.FrideInstanceConfig, list []doctorNode, have map[string]liveIdentity) []string {
	var problems []string
	now := env.Time()
	least := frideTries(ic.MemoryMB, frideSizeSmall)[0]
	tpl := frideSandboxTemplate(env.Getenv, version.String())
	var hosts, notReady, small []string
	for _, n := range list {
		if n.Device != nodeproto.DeviceApproved || n.Approved.FrideHost == nil || n.Declared.FrideHost == nil {
			continue
		}
		if len(ic.Nodes) > 0 && !slices.ContainsFunc(ic.Nodes, func(ref string) bool { return ref == n.ID || strings.EqualFold(ref, n.Name) }) {
			continue
		}
		fh := n.Declared.FrideHost
		switch {
		case n.stale(now):
			notReady = append(notReady, n.Name+" not heard from")
		case !fh.Ready:
			notReady = append(notReady, n.Name+": "+orDefault(fh.Problem, "its sandboxes are not ready"))
		case fh.NeedsTemplate && tpl == "":
			notReady = append(notReady, n.Name+": "+noTemplate)
		case fh.TotalMemoryMB > 0 && fh.TotalMemoryMB-fh.ReserveMB < least:
			small = append(small, fmt.Sprintf("%s (%d MB beyond its reserve)", n.Name, fh.TotalMemoryMB-fh.ReserveMB))
		default:
			hosts = append(hosts, n.Name)
		}
	}
	if len(hosts) == 0 {
		why := "no approved node offers FrIDE instances (fride_host)"
		if len(ic.Nodes) > 0 {
			why += " among " + strings.Join(ic.Nodes, ", ")
		}
		if more := slices.Concat(notReady, small); len(more) > 0 {
			why = fmt.Sprintf("no node can run it (%s)", strings.Join(more, "; "))
			if len(small) > 0 {
				why += fmt.Sprintf("; it needs %d MB at the least", least)
			}
		}
		problems = append(problems, why)
	}
	for _, id := range ic.Identities {
		switch live, known := have[id]; {
		case known && !live.set:
			problems = append(problems, "the core holds no "+id+" identity")
		case known && live.note != "":
			problems = append(problems, "the core cannot hand out its "+id+" identity: "+live.note)
		case !known && id == gitident.GitLab:
			if _, err := env.Stat(filepath.Join(config.Layout{Root: env.Config.DataDir}.LocalSecrets(), gitident.GitLabFile)); err != nil {
				problems = append(problems, "the core holds no gitlab identity")
			}
		}
	}
	return problems
}

// checkFrideInstances checks every instance; a warning names each one
// that cannot run, and why.
func checkFrideInstances(ctx context.Context, env *doctor.Env) doctor.Result {
	list, res := readNodes(ctx, env)
	if res != nil && res.Status != doctor.OK {
		return *res
	}
	have := liveIdentities(ctx, env)
	var ok, bad []string
	for _, name := range slices.Sorted(maps.Keys(env.Config.Fride.Instances)) {
		if p := frideInstanceProblems(env, name, env.Config.Fride.Instances[name], list, have); len(p) > 0 {
			bad = append(bad, name+": "+strings.Join(p, "; "))
		} else {
			ok = append(ok, name)
		}
	}
	if len(bad) > 0 {
		return doctor.Result{Status: doctor.Warn,
			Detail: fmt.Sprintf("%d of %d FrIDE instances cannot run: %s", len(bad), len(bad)+len(ok), strings.Join(bad, "; ")),
			Fix:    "on a node that is to run them, run fridolin node fride host setup and approve what it offers on the Nodes page (fride.instances.<name>.nodes names where each may run); connect the identities an instance names on the dashboard's Integrations page"}
	}
	return doctor.Result{Status: doctor.OK, Detail: "each can start on an approved node, with its identities: " + strings.Join(ok, ", ")}
}
