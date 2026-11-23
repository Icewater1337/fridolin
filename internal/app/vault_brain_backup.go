package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/backup"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// brainBackup is what a brain's backup settings (brain.json's "backup")
// leave out of everything the vault sends to the cloud: the restic
// snapshots, their copy on Drive, and the cold tier when the folder lies
// in the hot root. The Drive backup reads the same settings, and both
// mean the same by them (backup.ExcludePatterns).
type brainBackup struct {
	ID  string `json:"id"`
	Dir string `json:"dir"` // absolute
	// Skip says why the whole folder is left out; "" when it is backed up.
	Skip string `json:"skip,omitempty"`
	// Unreadable is set when it is left out because its brain.json could
	// not be read: the vault does not upload what might be excluded.
	Unreadable bool `json:"unreadable,omitempty"`
	// patterns are backup.ExcludePatterns, relative to Dir: what stays
	// out of the cloud.
	patterns []string
	// nodes are the node targets the folder goes to, by name, each with
	// the patterns that stay out of it (brainBackupNodes).
	nodes map[string][]string
	// policies are the node policies the folder's backups follow, as
	// approved and as the brain.json files on disk say: a target any of
	// them denies gets none of what it denies (#760).
	policies []manifest.Policy
}

// brainBackupOf reads the backup settings of the brain in dir: those of
// its brain.json on disk and, when there is one, of the manifest the
// bridge runs it with (approved), whichever leaves out more. A brain that
// edits its own brain.json cannot widen what goes to the cloud that way,
// nor can a change the bridge has not taken up yet narrow it.
//
// policies are the node policies to follow (backupPolicies); none given,
// the brain's own node_policy, approved and on disk.
func brainBackupOf(id, dir string, approved *manifest.Manifest, includeSecrets bool, policies ...manifest.Policy) brainBackup {
	b := brainBackup{ID: id, Dir: dir, policies: policies}
	if len(policies) == 0 {
		b.policies = backupPolicies([]vaultBrainRef{{Dir: dir, Approved: approved}}, HomeDir())
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		b.Skip = "the folder does not exist"
		return b
	}
	disk, err := manifest.Read(dir)
	if err != nil {
		b.Skip, b.Unreadable = "its brain.json cannot be read: "+err.Error(), true
		return b
	}
	return brainBackupWith(b, disk, approved, includeSecrets)
}

// brainBackupWith fills in b by the backup settings of disk and approved,
// whichever leaves out more.
func brainBackupWith(b brainBackup, disk, approved *manifest.Manifest, includeSecrets bool) brainBackup {
	if !disk.Backup.Enabled || (approved != nil && !approved.Backup.Enabled) {
		b.Skip = "its backup is off (backup.enabled)"
		return b
	}
	b.nodes = brainBackupNodes(disk, approved, includeSecrets)
	if !disk.Backup.Cloud() || (approved != nil && !approved.Backup.Cloud()) {
		b.Skip = "it does not go to the cloud (backup.targets.cloud)"
		return b
	}
	excludes := disk.Backup.CloudExcludes()
	if approved != nil {
		excludes = append(excludes, approved.Backup.CloudExcludes()...)
	}
	all, why, denied := b.policyDenied(manifest.TargetCloud)
	if all {
		b.Skip = "the node policy keeps it off the cloud: backups may go " + why
		return b
	}
	b.patterns = backup.ExcludePatterns(append(excludes, denied...), includeSecrets)
	return b
}

// policyDenied is what of the folder the node policies keep from the
// backup target keys names (manifest.Policy.BackupDenied): all of it,
// or the patterns of the folders below that stay out.
func (b brainBackup) policyDenied(keys ...string) (all bool, why string, patterns []string) {
	for _, p := range b.policies {
		a, w, pats := p.BackupDenied(b.Dir, keys...)
		if a {
			return true, w, nil
		}
		patterns = append(patterns, pats...)
	}
	return false, "", patterns
}

// BrainDirPolicies are the node policies of every folder in brain_dirs,
// approved and on disk (backupPolicies), for a core export.
func BrainDirPolicies(refs []config.BrainRef, home string) []manifest.Policy {
	out := make([]vaultBrainRef, 0, len(refs))
	for _, r := range refs {
		out = append(out, vaultBrainRef{Dir: r.Dir(home), Approved: r.Approved})
	}
	return backupPolicies(out, home)
}

// backupPolicies are the node policies of the brain folders refs, those
// approved and those of their brain.json on disk, each on its own: a
// rule written on disk can keep more out, never let more through.
func backupPolicies(refs []vaultBrainRef, home string) []manifest.Policy {
	var approved, disk manifest.Policy
	for _, r := range refs {
		dir := filepath.Clean(AbsPath(fsx.ExpandHome(r.Dir, home)))
		if r.Approved != nil {
			approved = append(approved, r.Approved.FolderRules(dir, home)...)
		}
		if m, err := manifest.Read(dir); err == nil {
			disk = append(disk, m.FolderRules(dir, home)...)
		}
	}
	return []manifest.Policy{approved, disk}
}

// brainBackupNodes are the node targets the folder goes to, each with
// what stays out of it. A node target gets the folder only when both the
// brain.json on disk and the manifest the bridge runs the brain with
// choose it: a brain that edits its own brain.json cannot send more to a
// node that way. Out stays what either excludes, less the paths both list
// in node_only, which go to the node targets and never to the cloud.
func brainBackupNodes(disk, approved *manifest.Manifest, includeSecrets bool) map[string][]string {
	if approved == nil {
		return nil
	}
	out := map[string][]string{}
	for key := range disk.Backup.Targets {
		name, ok := strings.CutPrefix(key, manifest.TargetNodePrefix)
		if !ok || !disk.Backup.Node(name) || !approved.Backup.Node(name) {
			continue
		}
		var excludes []string
		for _, x := range append(slices.Clone(disk.Backup.Exclude), approved.Backup.Exclude...) {
			if !slices.Contains(disk.Backup.NodeOnly, x) || !slices.Contains(approved.Backup.NodeOnly, x) {
				excludes = append(excludes, x)
			}
		}
		out[name] = backup.ExcludePatterns(excludes, includeSecrets)
	}
	return out
}

// toNode reports whether the folder goes to node target name, and the
// patterns that stay out of it. keys are what else the node policy may
// call the target by: its node's id and name.
func (b brainBackup) toNode(name string, keys ...string) ([]string, bool) {
	p, ok := b.nodes[name]
	if !ok || b.Unreadable {
		return p, false
	}
	all, _, denied := b.policyDenied(append(keys, name)...)
	if all {
		return nil, false
	}
	if len(denied) > 0 {
		p = append(slices.Clone(p), backup.ExcludePatterns(denied, true)...)
	}
	return p, true
}

// resticExcludes are restic's --exclude values for this brain: its
// folder when it is skipped, else its patterns, absolute.
func (b brainBackup) resticExcludes() []string {
	if b.Skip != "" {
		return []string{backup.ResticPath(b.Dir)}
	}
	return backup.ResticExcludes(b.Dir, b.patterns)
}

// excluded reports whether rel, relative to the brain folder, stays out.
func (b brainBackup) excluded(rel string) bool {
	return b.Skip != "" || backup.Excluded(rel, b.patterns)
}

// vaultBrainBackups reads the backup settings of every brain folder the
// vault may see: the loaded brains and every folder in brain_dirs (one
// whose brain.json is broken is not loaded, and is skipped). Keyed by
// the folder, absolute.
func vaultBrainBackups(brains []vaultBrain, refs []vaultBrainRef, includeSecrets bool, home string) map[string]brainBackup {
	out := map[string]brainBackup{}
	all := slices.Clone(refs)
	for _, br := range brains {
		m := br.Manifest
		all = append(all, vaultBrainRef{Dir: br.Dir, Approved: &m})
	}
	policies := backupPolicies(all, home)
	for _, br := range brains {
		dir := filepath.Clean(AbsPath(br.Dir))
		m := br.Manifest
		out[dir] = brainBackupOf(br.ID, dir, &m, includeSecrets, policies...)
	}
	for _, ref := range refs {
		dir := filepath.Clean(AbsPath(ref.Dir))
		if _, done := out[dir]; done || ref.Dir == "" {
			continue
		}
		id := filepath.Base(dir)
		if ref.Approved != nil && ref.Approved.ID != "" {
			id = ref.Approved.ID
		}
		out[dir] = brainBackupOf(id, dir, ref.Approved, includeSecrets, policies...)
	}
	return out
}

// vaultBrainRef is a brain_dirs entry: a folder and, once approved, its
// manifest.
type vaultBrainRef struct {
	Dir      string
	Approved *manifest.Manifest
}

// BrainFolderFilter is what of the brain folder dir may leave this machine
// through the cloud, by the same rules as the vault's backups: skip says
// why none of it may ("" when some does), and excluded whether a path
// below it, slash-separated, stays. A core export, which goes through the
// vault bucket, takes a brain's folder along by it. approvedOnly reads
// the approved manifest alone and not brain.json on disk (core export
// --use-approved): the owner chose to leave an unapproved edit aside. A
// folder that does not exist, or whose brain.json cannot be read, stays
// left out all the same.
//
// policies are every brain's node policies (BrainDirPolicies); none
// given, the brain's own.
func BrainFolderFilter(id, dir string, approved *manifest.Manifest, includeSecrets, approvedOnly bool, policies ...manifest.Policy) (skip string, excluded func(rel string) bool) {
	b := brainBackupOf(id, dir, approved, includeSecrets, policies...)
	if approvedOnly && approved != nil && !b.Unreadable {
		if _, err := os.Stat(dir); err == nil {
			// The approved node policy alone, as for the rest.
			b = brainBackupWith(brainBackup{ID: id, Dir: dir, policies: b.policies[:1]}, approved, approved, includeSecrets)
		}
	}
	return b.Skip, b.excluded
}
