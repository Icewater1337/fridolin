package app

import (
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"
)

// The vault's hot root (docs/DECISIONS.md, M3 wiring): the data dir
// itself, of which only these parts are vault files:
//
//   - config.json at its top,
//   - secrets/ (age-encrypted files), audit/ (JSONL) and vault/ (filed
//     documents and archived workspaces),
//   - the media folder, when media_dir lies in the data dir, and
//   - the brain folders that lie in the data dir (the container's
//     brains-folders/<name>/), less what their backup settings leave out
//     (brainBackup): all of a folder whose backup is off or whose
//     brain.json cannot be read.
//
// Never part of it: bridge.db and vault.db with their sidecars (Litestream
// replicates them, and restic gets VACUUM INTO copies; a live SQLite file
// copied by a file sync can be caught halfway through a write), the
// brains' own state under brains/ (CLI sign-ins, TMPDIR, caches), run
// tokens and records, the dashboard's token and sessions, the scripts
// token, the rclone, Google, Plaud, OTP, site, tsnet, nodes, core and
// backup folders, and lock files. restic's nightly snapshot covers what of
// those is worth keeping. Brain folders outside the data dir (a native
// install's ~/brains/...) are not vault files either: restic snapshots them.
var vaultTopParts = []string{"secrets", "audit", "vault"}

// vaultJunk are names inside the vault's folders that are never vault
// files: what a brain folder rebuilds or writes all the time.
var vaultJunk = []string{".git", "node_modules", ".venv", "__pycache__", ".DS_Store", brain.StreamLogName, brain.StreamLogName + ".1", ".cache"}

// vaultLayout says which paths below the hot root are vault files and
// which vault paths belong to which brain.
type vaultLayout struct {
	// root is the hot root, the data dir, absolute.
	root string
	// prefixes are the vault paths (relative to root) of the folders whose
	// files are vault files; files names the single files.
	prefixes []string
	files    []string
	// media is the media folder's vault path, "" when it lies outside the
	// data dir.
	media string
	// brainDirs maps a brain's id to its folder's vault path, for folders
	// inside the data dir.
	brainDirs map[string]string
	// absBrainDirs maps a brain's id to its folder, absolute, wherever it is.
	absBrainDirs map[string]string
	// backups are the backup settings of every brain folder, loaded or
	// not, by its absolute path (vaultBrainBackups); backupRels those of
	// the folders in the hot root, by vault path.
	backups    map[string]brainBackup
	backupRels map[string]brainBackup
}

// vaultLayoutOf works the hot root out of the settings, the brains (id →
// folder) and the backup settings of the brain folders (folder →
// settings). A loaded brain without backup settings there is left out
// whole: the vault never uploads what might be excluded.
func vaultLayoutOf(cfg *config.Settings, brains map[string]string, backups map[string]brainBackup) vaultLayout {
	l := vaultLayout{root: filepath.Clean(AbsPath(cfg.DataDir)), files: []string{"config.json"}, brainDirs: map[string]string{}, absBrainDirs: map[string]string{},
		backups: maps.Clone(backups), backupRels: map[string]brainBackup{}}
	if l.backups == nil {
		l.backups = map[string]brainBackup{}
	}
	l.prefixes = append(l.prefixes, vaultTopParts...)
	if rel, ok := l.rel(AbsPath(cfg.MediaDir)); ok && rel != "" {
		l.media = rel
		l.prefixes = append(l.prefixes, rel)
	}
	for id, dir := range brains {
		abs := filepath.Clean(AbsPath(dir))
		l.absBrainDirs[id] = abs
		if rel, ok := l.rel(abs); ok && rel != "" && !l.reserved(rel) {
			l.brainDirs[id] = rel
			l.prefixes = append(l.prefixes, rel)
		}
		if _, ok := l.backups[abs]; !ok {
			l.backups[abs] = brainBackup{ID: id, Dir: abs, Skip: "its backup settings were not read", Unreadable: true}
		}
	}
	for dir, b := range l.backups {
		if rel, ok := l.rel(dir); ok && rel != "" {
			l.backupRels[rel] = b
		}
	}
	slices.Sort(l.prefixes)
	l.prefixes = slices.Compact(l.prefixes)
	return l
}

// reserved reports whether rel lies in a part of the data dir that is
// never a vault file, whatever else names it.
func (l vaultLayout) reserved(rel string) bool {
	first, _, _ := strings.Cut(rel, "/")
	switch first {
	case brainStatesDirName, runTokensDirName, "runs", "core", "rclone", "google", "plaud", "otp", "site", "tsnet", "nodes", "backup":
		return true
	}
	return false
}

// rel is abs as a vault path, if it lies in the hot root ("" for the root
// itself).
func (l vaultLayout) rel(abs string) (string, bool) {
	if abs == "" || l.root == "" {
		return "", false
	}
	r, err := filepath.Rel(l.root, filepath.Clean(abs))
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", false
	}
	if r == "." {
		return "", true
	}
	return filepath.ToSlash(r), true
}

// ignore is vault.Options.Ignore: true for every path below the hot root
// that is not a vault file nor a folder on the way to one.
func (l vaultLayout) ignore(rel string) bool {
	if slices.Contains(l.files, rel) {
		return false
	}
	if l.backupExcluded(rel) {
		return true
	}
	for _, p := range l.prefixes {
		switch {
		case rel == p:
			return false
		case strings.HasPrefix(rel, p+"/"):
			for seg := range strings.SplitSeq(strings.TrimPrefix(rel, p+"/"), "/") {
				if slices.Contains(vaultJunk, seg) || strings.HasSuffix(seg, ".lock") {
					return true
				}
			}
			return false
		case strings.HasPrefix(p, rel+"/"):
			return false // a folder on the way to p
		}
	}
	return true
}

// backupExcluded reports whether rel lies in a brain folder whose backup
// settings leave it out (brainBackup).
func (l vaultLayout) backupExcluded(rel string) bool {
	for dir, b := range l.backupRels {
		if rel == dir {
			if b.Skip != "" {
				return true
			}
		} else if inside, ok := strings.CutPrefix(rel, dir+"/"); ok && b.excluded(inside) {
			return true
		}
	}
	return false
}

// brainPrefixes are the vault paths brain id owns (docs/DECISIONS.md, M3
// wiring): vault/documents/<id>, its media inbox when the media folder is
// in the hot root, and its folder when that is.
func (l vaultLayout) brainPrefixes(id string) []string {
	out := []string{path.Join("vault", "documents", id)}
	if l.media != "" {
		out = append(out, path.Join(l.media, id))
	}
	if d, ok := l.brainDirs[id]; ok {
		out = append(out, d)
	}
	return out
}
