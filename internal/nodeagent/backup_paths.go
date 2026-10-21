package nodeagent

import (
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// Every path the backup family touches is built here, and confined here,
// because it writes on the owner's machine what a request from the core
// says. A request names a folder and, for a file, a restic type and name:
//
//   - the folder must be one the owner approved, and what is used is the
//     approval's own spelling of it, never the request's, with its
//     symlinks resolved once; that real folder must lie under the place
//     the node lends for backups, resolved the same way, so a symlinked
//     folder on the way cannot lead elsewhere;
//   - a file's path is rebuilt from constants and the name's decoded
//     bytes (restic's exact layout: config, keys/<hex>, locks/<hex>,
//     snapshots/<hex>, index/<hex>, data/<2 hex>/<hex>); anything else is
//     refused before a path exists;
//   - right before each filesystem call the absolute path is checked to
//     lie in the real folder (inside), and the call then goes through an
//     *os.Root of it, which refuses every way out, .. and symlinks in the
//     folder included.

// inside reports whether p, cleaned and absolute, is base or lies below
// it.
func inside(base, p string) bool {
	base, p = filepath.Clean(base), filepath.Clean(p)
	if runtime.GOOS == "windows" {
		// Windows' file systems ignore case.
		base, p = strings.ToLower(base), strings.ToLower(p)
	}
	if !filepath.IsAbs(base) || !filepath.IsAbs(p) {
		return false
	}
	if p == base {
		return true
	}
	rel, err := filepath.Rel(base, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	prefix := base
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(p, prefix)
}

// realPath is p with the symlinks of its deepest existing part resolved;
// the part that does not exist yet is joined as it is.
func realPath(p string) (string, error) {
	p = filepath.Clean(p)
	var rest []string
	for cur := p; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(append([]string{real}, rest...)...), nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = parent
	}
}

// lentRoot is the real form of the lent root that holds p, "" with
// none named (every drive is lent); ok is false when p lies under none.
func lentRoot(eff nodeproto.Capabilities, p string) (root string, ok bool) {
	if eff.Backup == nil {
		return "", false
	}
	if len(eff.Backup.Roots) == 0 {
		return "", true
	}
	for _, r := range eff.Backup.Roots {
		real, err := realPath(r)
		if err != nil {
			continue
		}
		if inside(real, p) {
			return real, true
		}
	}
	return "", false
}

// confinedFolder is the approved backup folder the request names, real,
// after checking it lies under a lent root.
func confinedFolder(eff nodeproto.Capabilities, requested string) (string, *nodeproto.Error) {
	if requested == "" || !filepath.IsAbs(requested) {
		return "", nodeproto.Errorf(nodeproto.CodeBadRequest, "folder %q is not absolute", requested)
	}
	if eff.Backup == nil {
		return "", nodeproto.Errorf(nodeproto.CodeDenied, "backups are not approved on this node")
	}
	i := slices.IndexFunc(eff.Backup.Folders, func(f string) bool { return nodeproto.SamePath(f, requested) })
	if i < 0 {
		return "", nodeproto.Errorf(nodeproto.CodeDenied, "%s is not a backup folder the owner approved", requested)
	}
	approved := eff.Backup.Folders[i]
	if !filepath.IsAbs(approved) {
		return "", nodeproto.Errorf(nodeproto.CodeBadRequest, "folder %q is not absolute here", approved)
	}
	real, err := realPath(approved)
	if err != nil {
		return "", fsError(err, approved)
	}
	if _, ok := lentRoot(eff, real); !ok {
		return "", nodeproto.Errorf(nodeproto.CodeOutsideRoot, "%s leads out of the places this node lends for backups", approved)
	}
	return real, nil
}

// confinedBrowse is the folder to list, real, after checking it lies
// under a lent root.
func confinedBrowse(eff nodeproto.Capabilities, requested string) (string, *nodeproto.Error) {
	if requested == "" || !filepath.IsAbs(requested) || strings.ContainsRune(requested, 0) {
		return "", nodeproto.Errorf(nodeproto.CodeBadRequest, "path %q is not absolute", requested)
	}
	clean := filepath.Clean(requested)
	if !eff.AllowsBrowse(clean) {
		return "", nodeproto.Errorf(nodeproto.CodeOutsideRoot, "%s is not under a place this node lends for backups", clean)
	}
	real, err := realPath(clean)
	if err != nil {
		return "", fsError(err, clean)
	}
	root, ok := lentRoot(eff, real)
	if !ok || root != "" && !inside(root, real) {
		return "", nodeproto.Errorf(nodeproto.CodeOutsideRoot, "%s leads out of the places this node lends for backups", clean)
	}
	return real, nil
}

// fileRel is the file's path in its folder, rebuilt from constants and
// the name's decoded bytes: restic's exact layout, nothing else.
func fileRel(typ, name string) (string, error) {
	if err := nodeproto.CheckBackupFile(typ, name); err != nil {
		return "", err
	}
	if typ == nodeproto.BackupConfig {
		return nodeproto.BackupConfig, nil
	}
	raw, err := hex.DecodeString(name)
	if err != nil || len(raw) != 32 {
		return "", errors.New("backup: not a restic file name")
	}
	id := hex.EncodeToString(raw)
	switch typ {
	case nodeproto.BackupData:
		return filepath.Join(nodeproto.BackupData, id[:2], id), nil
	case nodeproto.BackupKeys:
		return filepath.Join(nodeproto.BackupKeys, id), nil
	case nodeproto.BackupLocks:
		return filepath.Join(nodeproto.BackupLocks, id), nil
	case nodeproto.BackupSnapshots:
		return filepath.Join(nodeproto.BackupSnapshots, id), nil
	case nodeproto.BackupIndex:
		return filepath.Join(nodeproto.BackupIndex, id), nil
	}
	return "", errors.New("backup: not a restic file type")
}

// typeDir is the folder of a listable type, a constant.
func typeDir(typ string) (string, bool) {
	switch typ {
	case nodeproto.BackupData:
		return nodeproto.BackupData, true
	case nodeproto.BackupKeys:
		return nodeproto.BackupKeys, true
	case nodeproto.BackupLocks:
		return nodeproto.BackupLocks, true
	case nodeproto.BackupSnapshots:
		return nodeproto.BackupSnapshots, true
	case nodeproto.BackupIndex:
		return nodeproto.BackupIndex, true
	}
	return "", false
}

// uploadRel is an upload's temp file in the folder, rebuilt from the
// ID's decoded bytes.
func uploadRel(id string) (string, error) {
	if err := nodeproto.CheckUpload(id); err != nil {
		return "", err
	}
	raw, err := hex.DecodeString(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(uploadsDir, hex.EncodeToString(raw)), nil
}

// repo is an approved backup folder, open.
type repo struct {
	// dir is the folder, real.
	dir  string
	root *os.Root
}

// at checks that rel, relative to the folder, stays in it, and returns
// rel for the *os.Root's calls.
func (r repo) at(rel string) (string, *nodeproto.Error) {
	abs := filepath.Join(r.dir, rel)
	if filepath.IsAbs(rel) || !inside(r.dir, abs) {
		return "", nodeproto.Errorf(nodeproto.CodeOutsideRoot, "%s leads out of the backup folder", rel)
	}
	return rel, nil
}

func (r repo) close() { _ = r.root.Close() }

// openFolder opens the approved folder the request names.
func openFolder(eff nodeproto.Capabilities, requested string) (repo, *nodeproto.Error) {
	dir, perr := confinedFolder(eff, requested)
	if perr != nil {
		return repo{}, perr
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return repo{}, fsError(err, dir)
	}
	return repo{dir: dir, root: root}, nil
}
