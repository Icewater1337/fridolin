package nodeproto

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// BackupCap lets the core keep restic repositories on the node
// (docs/NODES.md, "Backups"). The node declares it with the places it
// lends for backups (Roots); the owner approves it like exec and fs, and
// then picks a folder for each backup target, which the core adds to the
// approval (Folders). The node writes only restic's own files, by
// restic's own names, below an approved folder: the core sends the data,
// already encrypted by restic on the core, and never a path of its own.
//
// fs.roots and fs.browse, which the dashboard's folder picker uses, come
// with the approval: they list drives, volumes and folder names under
// Roots, and never a file or a file's content.
type BackupCap struct {
	// Roots are the drives or folders under which the core may browse and
	// pick backup folders; none is every local drive and volume.
	Roots []string `json:"roots,omitempty"`
	// Folders are the backup folders the owner picked, one per target.
	// Only the core's approval holds them: a node's declaration has none.
	Folders []string `json:"folders,omitempty"`
}

// Bounds of a backup declaration.
const (
	maxBackupRoots   = 32
	maxBackupFolders = 64
)

// Check reports what is wrong with the declaration.
func (b *BackupCap) Check() error {
	if b == nil {
		return nil
	}
	if len(b.Roots) > maxBackupRoots {
		return fmt.Errorf("backup: at most %d roots", maxBackupRoots)
	}
	if len(b.Folders) > maxBackupFolders {
		return fmt.Errorf("backup: at most %d folders", maxBackupFolders)
	}
	for _, p := range append(slices.Clone(b.Roots), b.Folders...) {
		if err := CheckAbsPath(p); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}
	return nil
}

// CheckAbsPath reports whether p is an absolute path, in Unix or Windows
// spelling, of a bounded length and without a NUL.
func CheckAbsPath(p string) error {
	if p == "" || len(p) > maxPathLen || strings.ContainsRune(p, 0) || !isAbs(p) {
		return fmt.Errorf("%q must be an absolute path", p)
	}
	return nil
}

// windowsPath reports whether p is spelled the Windows way: a drive
// letter or a UNC path.
func windowsPath(p string) bool {
	return strings.HasPrefix(p, `\\`) || len(p) >= 2 && p[1] == ':' && (p[0]|0x20) >= 'a' && (p[0]|0x20) <= 'z'
}

// normPath is p cleaned, with forward slashes, lower case for a Windows
// path, whose file systems ignore case: what PathWithin compares.
func normPath(p string) string {
	if windowsPath(p) {
		p = strings.ToLower(strings.ReplaceAll(p, `\`, "/"))
		if strings.HasPrefix(p, "//") {
			return "//" + strings.TrimPrefix(path.Clean("/"+p[2:]), "/")
		}
		// "c:" + the rest, cleaned as a rooted path.
		return p[:2] + path.Clean("/"+p[2:])
	}
	return path.Clean(p)
}

// PathWithin reports whether p is base or lies below it, comparing the
// way the node's OS would: the core checks paths of nodes of any OS.
func PathWithin(base, p string) bool {
	if windowsPath(base) != windowsPath(p) {
		return false
	}
	b, q := normPath(base), normPath(p)
	if q == b {
		return true
	}
	if !strings.HasSuffix(b, "/") {
		b += "/"
	}
	return strings.HasPrefix(q, b)
}

// SamePath reports whether a and b name the same folder on the node.
func SamePath(a, b string) bool {
	return windowsPath(a) == windowsPath(b) && normPath(a) == normPath(b)
}

// withinRoots reports whether p lies under one of roots; no roots is
// anywhere.
func withinRoots(roots []string, p string) bool {
	return len(roots) == 0 || slices.ContainsFunc(roots, func(r string) bool { return PathWithin(r, p) })
}

// AllowsBackupFolder reports whether c lets the core keep a repository in
// folder: backup is approved and folder is one of its folders.
func (c Capabilities) AllowsBackupFolder(folder string) bool {
	return c.Backup != nil && slices.ContainsFunc(c.Backup.Folders, func(f string) bool { return SamePath(f, folder) })
}

// AllowsBrowse reports whether c lets the core list the folders of p.
func (c Capabilities) AllowsBrowse(p string) bool {
	return c.Backup != nil && withinRoots(c.Backup.Roots, p)
}

// coversBackup reports whether the approved backup capability covers the
// declared one: approved at all, and for every place the declaration
// lends, approved for it too (an approval for every drive covers any).
func coversBackup(approved, declared *BackupCap) bool {
	if declared == nil {
		return true
	}
	if approved == nil {
		return false
	}
	if len(approved.Roots) == 0 {
		return true
	}
	if len(declared.Roots) == 0 {
		return false
	}
	for _, r := range declared.Roots {
		if !slices.ContainsFunc(approved.Roots, func(a string) bool { return SamePath(a, r) }) {
			return false
		}
	}
	return true
}

// intersectBackup is the part of declared that approved covers: the
// declared roots the approval names (all of them when it names none), and
// the approved folders that lie under what remains.
func intersectBackup(declared, approved *BackupCap) *BackupCap {
	if declared == nil || approved == nil {
		return nil
	}
	out := &BackupCap{}
	switch {
	case len(approved.Roots) == 0:
		out.Roots = slices.Clone(declared.Roots)
	case len(declared.Roots) == 0:
		out.Roots = slices.Clone(approved.Roots)
	default:
		for _, r := range declared.Roots {
			if slices.ContainsFunc(approved.Roots, func(a string) bool { return SamePath(a, r) }) {
				out.Roots = append(out.Roots, r)
			}
		}
		if len(out.Roots) == 0 {
			return nil
		}
	}
	for _, f := range approved.Folders {
		if withinRoots(out.Roots, f) {
			out.Folders = append(out.Folders, f)
		}
	}
	return out
}

// The methods of the backup family. All need backup approved; the
// repository methods also a folder of the approval.
const (
	// MethodFSRoots lists the node's drives and volumes (FSRootsResult).
	MethodFSRoots = "fs.roots"
	// MethodFSBrowse lists the folder names of a folder (BrowseParams →
	// BrowseResult): never a file, never content.
	MethodFSBrowse = "fs.browse"
	// MethodBackupInfo says whether a folder holds a repository, its size
	// and the space left (BackupFolderParams → BackupInfo).
	MethodBackupInfo = "backup.info"
	// MethodBackupCreate makes a folder a repository's: the folder and
	// restic's subfolders (BackupFolderParams).
	MethodBackupCreate = "backup.create"
	// MethodBackupStat is the size of one file (BackupFileParams →
	// BackupFile); a missing one is CodeNotFound.
	MethodBackupStat = "backup.stat"
	// MethodBackupList lists the files of one type (BackupFileParams with
	// no name → BackupListResult).
	MethodBackupList = "backup.list"
	// MethodBackupRead reads part of a file (BackupReadParams →
	// BackupReadResult).
	MethodBackupRead = "backup.read"
	// MethodBackupWrite writes a file in chunks (BackupWriteParams →
	// BackupFile).
	MethodBackupWrite = "backup.write"
	// MethodBackupDelete removes a file (BackupFileParams).
	MethodBackupDelete = "backup.delete"
)

// BackupMethods are the methods the backup family grants.
var BackupMethods = []string{MethodFSRoots, MethodFSBrowse, MethodBackupInfo, MethodBackupCreate,
	MethodBackupStat, MethodBackupList, MethodBackupRead, MethodBackupWrite, MethodBackupDelete}

// BackupRepoMethods are those that touch a repository: the core's backups
// call them, never the dashboard's generic call.
var BackupRepoMethods = []string{MethodBackupInfo, MethodBackupCreate, MethodBackupStat, MethodBackupList,
	MethodBackupRead, MethodBackupWrite, MethodBackupDelete}

// MaxBackupChunk is the most one backup.read or backup.write carries.
const MaxBackupChunk = 8 << 20

// The file types of a restic repository, as its REST protocol names them.
const (
	BackupConfig    = "config"
	BackupData      = "data"
	BackupKeys      = "keys"
	BackupLocks     = "locks"
	BackupSnapshots = "snapshots"
	BackupIndex     = "index"
)

// BackupTypes are restic's file types; each but config is a folder.
var BackupTypes = []string{BackupConfig, BackupData, BackupKeys, BackupLocks, BackupSnapshots, BackupIndex}

// backupNameRe is a restic file name: the SHA-256 of its content, hex.
var backupNameRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// CheckBackupFile reports whether typ and name name a file restic keeps:
// config has no name, every other type a 64-digit hex one.
func CheckBackupFile(typ, name string) error {
	switch {
	case !slices.Contains(BackupTypes, typ):
		return fmt.Errorf("backup: no file type %q", typ)
	case typ == BackupConfig && name != "":
		return errors.New("backup: config has no name")
	case typ != BackupConfig && !backupNameRe.MatchString(name):
		return fmt.Errorf("backup: %q is not a restic file name", name)
	}
	return nil
}

// BackupRelPath is where restic keeps the file in its folder, slash
// separated: config, data/ab/ab12…, keys/…, and so on.
func BackupRelPath(typ, name string) string {
	switch typ {
	case BackupConfig:
		return BackupConfig
	case BackupData:
		return BackupData + "/" + name[:2] + "/" + name
	}
	return typ + "/" + name
}

// FSVolume is one drive or volume of the node.
type FSVolume struct {
	// Path is the volume's root: C:\, /, /Volumes/Backup, /mnt/disk.
	Path string `json:"path"`
	// Label names it for a person: "Backup", "Home", "C:".
	Label string `json:"label,omitempty"`
	// Free is the space left, in bytes; 0 where unknown.
	Free uint64 `json:"free,omitempty"`
	// Removable is an external or network disk that may be away.
	Removable bool `json:"removable,omitempty"`
}

// FSRootsResult lists the node's volumes.
type FSRootsResult struct {
	// Sep is the node's path separator, "\\" or "/".
	Sep   string     `json:"sep"`
	Roots []FSVolume `json:"roots"`
}

// BrowseParams names a folder to list.
type BrowseParams struct {
	Path string `json:"path"`
	// Hidden lists folders whose names start with a dot too.
	Hidden bool `json:"hidden,omitempty"`
}

// BrowseResult is a folder's folders, by name, sorted.
type BrowseResult struct {
	Path string `json:"path"`
	// Parent is the folder above, "" at a volume's root.
	Parent  string   `json:"parent,omitempty"`
	Folders []string `json:"folders"`
	// Repo says the folder holds a restic repository already.
	Repo bool `json:"repo,omitempty"`
	// Truncated says there were more folders than a listing holds.
	Truncated bool `json:"truncated,omitempty"`
}

// BackupFolderParams names a backup folder.
type BackupFolderParams struct {
	Folder string `json:"folder"`
}

// BackupInfo is a backup folder's state.
type BackupInfo struct {
	Folder string `json:"folder"`
	// Exists says the folder is there, Repo that it holds a repository's
	// config.
	Exists bool `json:"exists"`
	Repo   bool `json:"repo"`
	// Size is what the repository's files take, Files how many there are.
	Size  int64 `json:"size"`
	Files int   `json:"files"`
	// Free is the space left on its volume; 0 where unknown.
	Free uint64 `json:"free,omitempty"`
}

// BackupFileParams names a file of a repository, or with no name, a type.
type BackupFileParams struct {
	Folder string `json:"folder"`
	Type   string `json:"type"`
	Name   string `json:"name,omitempty"`
}

// BackupFile is one file of a repository.
type BackupFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// BackupListResult is the files of one type.
type BackupListResult struct {
	Files []BackupFile `json:"files"`
}

// BackupReadParams reads Length bytes from Offset (Length 0: to the end,
// at most MaxBackupChunk).
type BackupReadParams struct {
	BackupFileParams
	Offset int64 `json:"offset"`
	Length int64 `json:"length,omitempty"`
}

// BackupReadResult is a part of a file.
type BackupReadResult struct {
	Data []byte `json:"data"`
	// Size is the whole file's size.
	Size int64 `json:"size"`
}

// BackupWriteParams carries one chunk of a new file. The chunks of one
// upload share Upload, an ID the core makes, and come in order; the last
// has Final, with the file's Size and SHA-256, and only then the file
// appears under its name. A file that is there already is not replaced.
type BackupWriteParams struct {
	BackupFileParams
	Upload string `json:"upload"`
	Offset int64  `json:"offset"`
	Data   []byte `json:"data"`
	Final  bool   `json:"final,omitempty"`
	Size   int64  `json:"size,omitempty"`
	// SHA256 is the whole file's hash, hex, on the final chunk.
	SHA256 string `json:"sha256,omitempty"`
}

// uploadRe is an upload ID: what a temp file may be named after.
var uploadRe = regexp.MustCompile(`^[0-9a-f]{16,64}$`)

// CheckUpload reports whether id is an upload ID.
func CheckUpload(id string) error {
	if !uploadRe.MatchString(id) {
		return fmt.Errorf("backup: upload %q is not an upload ID", id)
	}
	return nil
}
