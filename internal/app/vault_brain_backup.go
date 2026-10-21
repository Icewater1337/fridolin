package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/Icewater1337/fridolin/internal/backup"
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
	// patterns are backup.ExcludePatterns, relative to Dir.
	patterns []string
}

// brainBackupOf reads the backup settings of the brain in dir: those of
// its brain.json on disk and, when there is one, of the manifest the
// bridge runs it with (approved), whichever leaves out more. A brain that
// edits its own brain.json cannot widen what goes to the cloud that way,
// nor can a change the bridge has not taken up yet narrow it.
func brainBackupOf(id, dir string, approved *manifest.Manifest, includeSecrets bool) brainBackup {
	b := brainBackup{ID: id, Dir: dir}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		b.Skip = "the folder does not exist"
		return b
	}
	disk, err := manifest.Read(dir)
	if err != nil {
		b.Skip, b.Unreadable = "its brain.json cannot be read: "+err.Error(), true
		return b
	}
	if !disk.Backup.Enabled || (approved != nil && !approved.Backup.Enabled) {
		b.Skip = "its backup is off (backup.enabled)"
		return b
	}
	excludes := slices.Clone(disk.Backup.Exclude)
	if approved != nil {
		excludes = append(excludes, approved.Backup.Exclude...)
	}
	b.patterns = backup.ExcludePatterns(excludes, includeSecrets)
	return b
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
func vaultBrainBackups(brains []vaultBrain, refs []vaultBrainRef, includeSecrets bool) map[string]brainBackup {
	out := map[string]brainBackup{}
	for _, br := range brains {
		dir := filepath.Clean(AbsPath(br.Dir))
		m := br.Manifest
		out[dir] = brainBackupOf(br.ID, dir, &m, includeSecrets)
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
		out[dir] = brainBackupOf(id, dir, ref.Approved, includeSecrets)
	}
	return out
}

// vaultBrainRef is a brain_dirs entry: a folder and, once approved, its
// manifest.
type vaultBrainRef struct {
	Dir      string
	Approved *manifest.Manifest
}
