package app

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/files"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

// The per-folder node policy (#760; docs/FILES.md, "Node policy"):
// brain.json's node_policy says, for a folder and everything inside it,
// where its copies may land and which nodes may run which work on it.
// Every place that picks a node or a backup target asks it here, and
// never falls back to a node it denies: the work waits or fails, saying
// why, and the log says what was skipped.

// nodePolicy is every running brain's approved node_policy: what a
// brain wrote into its brain.json counts once the owner approved it.
func (b *App) nodePolicy() manifest.Policy {
	if b.brainMgr == nil {
		return nil
	}
	home := b.cfgs.Get().Boot.Home
	var p manifest.Policy
	for _, br := range b.brainMgr.Brains() {
		p = append(p, br.Manifest().FolderRules(AbsPath(br.Dir()), home)...)
	}
	return p
}

// nodeKeys are what a policy may call node v by: its id and its name.
func nodeKeys(v nodes.View) []string { return []string{v.ID, v.Name} }

// policyDenied logs and says why the policy keeps work of kind on what
// (a folder, a repository) off node v.
func policyDenied(kind, what string, v nodes.View, l manifest.Limit) error {
	log.Printf("node policy: skipped %s for %s of %s: %s may run %s", v.Name, kind, what, kind, l.Text())
	return fmt.Errorf("the node policy keeps %s of %s off %s: it may run %s", kind, what, v.Name, l.Text())
}

// checkFolderTask is nil when node v may run work of kind in folder (on
// v), else why not, logged.
func (b *App) checkFolderTask(kind, folder string, v nodes.View) error {
	if l := b.nodePolicy().Task(kind, folder); !l.Allows(nodeKeys(v)...) {
		return policyDenied(kind, folder, v, l)
	}
	return nil
}

// repoLimits are the limits on coding work in repos.
func (b *App) repoLimits(repos []string) []manifest.Limit {
	if len(repos) == 0 {
		return nil
	}
	return b.nodePolicy().RepoTask(manifest.TaskCoding, repos)
}

// allowedForRepos keeps of cands the nodes that may run coding work in
// repos, logging each it skips; why says what the policy allows when it
// kept none back, "" when it skipped none.
func allowedForRepos(cands []nodes.View, limits []manifest.Limit, repos []string) (kept []nodes.View, why string) {
	if len(limits) == 0 {
		return cands, ""
	}
	var skipped []string
	for _, v := range cands {
		if l, deny := manifest.Denying(limits, nodeKeys(v)...); deny {
			log.Printf("node policy: skipped %s for coding in %s: coding may run %s", v.Name, strings.Join(repos, ", "), l.Text())
			skipped = append(skipped, v.Name)
			continue
		}
		kept = append(kept, v)
	}
	var allow []string
	for _, l := range limits {
		allow = append(allow, l.Text())
	}
	if len(skipped) == 0 {
		return kept, ""
	}
	return kept, fmt.Sprintf("the node policy lets coding in %s run %s, not on %s", strings.Join(repos, ", "), strings.Join(allow, "; "), strings.Join(skipped, ", "))
}

// movePolicyProblems are what the node policy has against moving the
// core to node id (name): a brain folder, or a folder in one, that the
// move would take along (it may go to the cloud, through which the export
// travels) and whose storage does not allow that node. A folder the
// policy keeps off the cloud stays home, and does not block.
func (b *App) movePolicyProblems(id, name string) []string {
	if b.brainMgr == nil {
		return nil
	}
	p := b.nodePolicy()
	var out []string
	for _, br := range b.brainMgr.Brains() {
		if !br.Manifest().Backup.Cloud() {
			continue // its folder stays home
		}
		dir := manifest.PolicyPath(AbsPath(br.Dir()))
		at := []string{dir}
		for _, r := range p {
			if strings.HasPrefix(r.Path, strings.TrimSuffix(dir, "/")+"/") {
				at = append(at, r.Path)
			}
		}
		for _, f := range at {
			l := p.Storage(f)
			if _, offCloud := manifest.Denying(p.Backup(f), manifest.TargetCloud); offCloud || l.Allows(id, name) {
				continue
			}
			log.Printf("node policy: moving the core to %s would take %s there, which may live %s", name, f, l.Text())
			out = append(out, fmt.Sprintf("the node policy lets %s live %s, and the move would take it to %s", f, l.Text(), name))
		}
	}
	return out
}

// policyFolder is where folder rel of root lies, as the node policy
// names it: the brain whose brain.json holds its rule and the rule's
// folder (relative to a brain's folder; for a node's, absolute, in the
// core brain's), and the folder's absolute path.
func (b *App) policyFolder(root files.Root, rel string) (brainID, folder, abs string, err error) {
	if root.Kind == files.KindBrain {
		return root.Brain, rel, filepath.Join(root.Path, filepath.FromSlash(rel)), nil
	}
	abs = root.Path
	if rel != "" {
		sep := "/"
		if strings.Contains(root.Path, `\`) {
			sep = `\`
		}
		abs = strings.TrimSuffix(root.Path, sep) + sep + strings.ReplaceAll(rel, "/", sep)
	}
	if b.brainMgr != nil {
		for _, br := range b.brainMgr.Brains() {
			if br.Manifest().Role == manifest.RoleCore {
				return br.ID(), abs, abs, nil
			}
		}
	}
	return "", "", abs, apperr.New(apperr.Invalid, "a node folder's node policy is kept in the core brain's brain.json, and there is no core brain")
}

// filesNodePolicy is the Files page's view of folder rel's node policy.
func (b *App) filesNodePolicy(root files.Root, rel string) *httpapi.NodePolicyView {
	brainID, folder, abs, err := b.policyFolder(root, rel)
	v := &httpapi.NodePolicyView{Folder: abs, Effective: map[string]httpapi.NodeLimitView{}}
	if err == nil && b.brainMgr != nil {
		for _, br := range b.brainMgr.Brains() {
			if br.ID() != brainID {
				continue
			}
			for _, r := range br.Manifest().NodePolicy {
				if r.Folder == folder {
					v.Rule = &r
				}
			}
		}
	}
	p := b.nodePolicy()
	limits := map[string]manifest.Limit{"storage": p.Storage(abs)}
	for _, kind := range manifest.NodeTaskKinds() {
		limits[kind] = p.Task(kind, abs)
	}
	for k, l := range limits {
		if l.Set {
			v.Effective[k] = httpapi.NodeLimitView{Nodes: append([]string{}, l.Nodes...), From: l.Folder, Brains: l.Brain}
		}
	}
	return v
}

// setFilesNodeRule sets folder rel's own rule, from the Files page.
func (b *App) setFilesNodeRule(root files.Root, rel string, rule manifest.NodeRule) error {
	brainID, folder, _, err := b.policyFolder(root, rel)
	if err != nil {
		return err
	}
	rule.Folder = folder
	if err := b.registry.SetNodeRule(brainID, rule); err != nil {
		return err
	}
	log.Printf("node policy: the owner set the rule of %s in brain %s: storage %v, tasks %v, repos %v", folder, brainID, rule.Storage, rule.Tasks, rule.Repos)
	return nil
}
