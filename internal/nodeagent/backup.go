package nodeagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// The backup family (docs/NODES.md, "Backups"): restic repositories the
// core keeps in folders the owner picked, and the read-only listing of
// drives and folder names the dashboard's picker shows. The core runs
// restic and sends the files restic writes, encrypted already; the node
// stores them by restic's own names and never runs restic itself.

// uploadsDir is the folder in a backup folder where a file being uploaded
// waits until its last chunk arrived and its hash checked out.
const uploadsDir = ".fridolin-uploads"

// maxBrowse bounds a folder listing.
const maxBrowse = 5000

// staleUpload is how old a half-uploaded file may grow before it is
// removed: the run that wrote it is long gone.
const staleUpload = 24 * time.Hour

func (a *Agent) backupCall(eff nodeproto.Capabilities, method string, params json.RawMessage) (any, *nodeproto.Error) {
	decode := func(v any) *nodeproto.Error {
		if len(params) == 0 {
			return nil
		}
		if err := json.Unmarshal(params, v); err != nil {
			return nodeproto.Errorf(nodeproto.CodeBadRequest, "%s params: %v", method, err)
		}
		return nil
	}
	switch method {
	case nodeproto.MethodFSRoots:
		return a.roots(eff), nil
	case nodeproto.MethodFSBrowse:
		var p nodeproto.BrowseParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return browse(eff, p)
	case nodeproto.MethodBackupInfo, nodeproto.MethodBackupCreate:
		var p nodeproto.BackupFolderParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		if e := backupFolder(eff, p.Folder); e != nil {
			return nil, e
		}
		if method == nodeproto.MethodBackupCreate {
			return a.backupCreate(p.Folder)
		}
		return backupInfo(p.Folder), nil
	case nodeproto.MethodBackupWrite:
		var p nodeproto.BackupWriteParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.backupWrite(eff, p)
	case nodeproto.MethodBackupRead:
		var p nodeproto.BackupReadParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return backupRead(eff, p)
	}
	var p nodeproto.BackupFileParams
	if e := decode(&p); e != nil {
		return nil, e
	}
	switch method {
	case nodeproto.MethodBackupStat:
		return backupStat(eff, p)
	case nodeproto.MethodBackupList:
		return backupList(eff, p)
	case nodeproto.MethodBackupDelete:
		return a.backupDelete(eff, p)
	}
	return nil, nodeproto.Errorf(nodeproto.CodeUnsupported, "no method %q", method)
}

// roots lists the drives and volumes, or, where the node lends only some
// places for backups, those.
func (a *Agent) roots(eff nodeproto.Capabilities) nodeproto.FSRootsResult {
	out := nodeproto.FSRootsResult{Sep: string(filepath.Separator), Roots: []nodeproto.FSVolume{}}
	var vols []platform.Volume
	if len(eff.Backup.Roots) > 0 {
		for _, r := range eff.Backup.Roots {
			vols = append(vols, platform.Volume{Path: r, Label: filepath.Base(r)})
		}
	} else if a.Volumes != nil {
		vols = a.Volumes()
	} else {
		vols = platform.Volumes()
	}
	for _, v := range vols {
		if st, err := os.Stat(v.Path); err != nil || !st.IsDir() {
			continue
		}
		fv := nodeproto.FSVolume{Path: v.Path, Label: v.Label, Removable: v.Removable}
		if free, err := platform.FreeBytes(v.Path); err == nil {
			fv.Free = free
		}
		out.Roots = append(out.Roots, fv)
	}
	return out
}

// systemFolders are folders a picker leaves out on Windows.
var systemFolders = []string{"$recycle.bin", "system volume information", "$windows.~bt", "$windows.~ws", "recovery"}

// browse lists the names of the folders in p: never a file.
func browse(eff nodeproto.Capabilities, p nodeproto.BrowseParams) (any, *nodeproto.Error) {
	if p.Path == "" || !filepath.IsAbs(p.Path) {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "path %q is not absolute", p.Path)
	}
	path := filepath.Clean(p.Path)
	if !eff.AllowsBrowse(path) {
		return nil, nodeproto.Errorf(nodeproto.CodeOutsideRoot, "%s is not under a place this node lends for backups", path)
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		return nil, fsError(err, path)
	}
	out := nodeproto.BrowseResult{Path: path, Folders: []string{}}
	if parent := filepath.Dir(path); parent != path && eff.AllowsBrowse(parent) {
		out.Parent = parent
	}
	for _, e := range ents {
		name := e.Name()
		if !e.IsDir() || !p.Hidden && strings.HasPrefix(name, ".") ||
			runtime.GOOS == "windows" && slices.Contains(systemFolders, strings.ToLower(name)) {
			continue
		}
		if len(out.Folders) == maxBrowse {
			out.Truncated = true
			break
		}
		out.Folders = append(out.Folders, name)
	}
	slices.SortFunc(out.Folders, func(x, y string) int { return strings.Compare(strings.ToLower(x), strings.ToLower(y)) })
	out.Repo = isRepo(path)
	return out, nil
}

// isRepo reports whether dir holds a restic repository: its config file
// and its keys folder.
func isRepo(dir string) bool {
	c, err := os.Stat(filepath.Join(dir, nodeproto.BackupConfig))
	k, kerr := os.Stat(filepath.Join(dir, nodeproto.BackupKeys))
	return err == nil && c.Mode().IsRegular() && kerr == nil && k.IsDir()
}

// backupFolder checks that the approval names folder.
func backupFolder(eff nodeproto.Capabilities, folder string) *nodeproto.Error {
	if folder == "" || !filepath.IsAbs(folder) {
		return nodeproto.Errorf(nodeproto.CodeBadRequest, "folder %q is not absolute", folder)
	}
	if !eff.AllowsBackupFolder(folder) {
		return nodeproto.Errorf(nodeproto.CodeDenied, "%s is not a backup folder the owner approved", folder)
	}
	return nil
}

// openRepo opens the backup folder for a file of it, after checking both.
func openRepo(eff nodeproto.Capabilities, p nodeproto.BackupFileParams, list bool) (*os.Root, string, *nodeproto.Error) {
	if e := backupFolder(eff, p.Folder); e != nil {
		return nil, "", e
	}
	var rel string
	if list {
		if !slices.Contains(nodeproto.BackupTypes, p.Type) || p.Type == nodeproto.BackupConfig || p.Name != "" {
			return nil, "", nodeproto.Errorf(nodeproto.CodeBadRequest, "backup: cannot list %q", p.Type)
		}
		rel = p.Type
	} else {
		if err := nodeproto.CheckBackupFile(p.Type, p.Name); err != nil {
			return nil, "", nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
		}
		rel = filepath.FromSlash(nodeproto.BackupRelPath(p.Type, p.Name))
	}
	root, err := os.OpenRoot(p.Folder)
	if err != nil {
		return nil, "", fsError(err, p.Folder)
	}
	return root, rel, nil
}

func (a *Agent) backupCreate(folder string) (any, *nodeproto.Error) {
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return nil, fsError(err, folder)
	}
	root, err := os.OpenRoot(folder)
	if err != nil {
		return nil, fsError(err, folder)
	}
	defer root.Close()
	dirs := []string{nodeproto.BackupKeys, nodeproto.BackupLocks, nodeproto.BackupSnapshots, nodeproto.BackupIndex, nodeproto.BackupData, uploadsDir}
	for i := range 256 {
		dirs = append(dirs, filepath.Join(nodeproto.BackupData, hex.EncodeToString([]byte{byte(i)})))
	}
	for _, d := range dirs {
		if err := root.MkdirAll(d, 0o700); err != nil {
			return nil, fsError(err, filepath.Join(folder, d))
		}
	}
	pruneUploads(root, time.Now())
	a.logf("node: made the backup folder %s", folder)
	return backupInfo(folder), nil
}

// pruneUploads removes uploads left over from runs that ended halfway.
func pruneUploads(root *os.Root, now time.Time) {
	f, err := root.Open(uploadsDir)
	if err != nil {
		return
	}
	ents, _ := f.ReadDir(-1)
	f.Close()
	for _, e := range ents {
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > staleUpload {
			_ = root.Remove(filepath.Join(uploadsDir, e.Name()))
		}
	}
}

func backupInfo(folder string) nodeproto.BackupInfo {
	out := nodeproto.BackupInfo{Folder: folder}
	st, err := os.Stat(folder)
	if err != nil || !st.IsDir() {
		return out
	}
	out.Exists, out.Repo = true, isRepo(folder)
	if free, err := platform.FreeBytes(folder); err == nil {
		out.Free = free
	}
	if root, err := os.OpenRoot(folder); err == nil {
		_ = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && p == uploadsDir {
				return fs.SkipDir
			}
			if d.Type().IsRegular() {
				if info, err := d.Info(); err == nil {
					out.Size += info.Size()
					out.Files++
				}
			}
			return nil
		})
		root.Close()
	}
	return out
}

func backupStat(eff nodeproto.Capabilities, p nodeproto.BackupFileParams) (any, *nodeproto.Error) {
	root, rel, perr := openRepo(eff, p, false)
	if perr != nil {
		return nil, perr
	}
	defer root.Close()
	st, err := root.Lstat(rel)
	if err != nil {
		return nil, fsError(err, filepath.Join(p.Folder, rel))
	}
	if !st.Mode().IsRegular() {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%s is not a file", rel)
	}
	return nodeproto.BackupFile{Name: p.Name, Size: st.Size()}, nil
}

func backupList(eff nodeproto.Capabilities, p nodeproto.BackupFileParams) (any, *nodeproto.Error) {
	root, rel, perr := openRepo(eff, p, true)
	if perr != nil {
		return nil, perr
	}
	defer root.Close()
	out := nodeproto.BackupListResult{Files: []nodeproto.BackupFile{}}
	dirs := []string{rel}
	if p.Type == nodeproto.BackupData {
		dirs = nil
		f, err := root.Open(rel)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return out, nil
			}
			return nil, fsError(err, filepath.Join(p.Folder, rel))
		}
		ents, _ := f.ReadDir(-1)
		f.Close()
		for _, e := range ents {
			if e.IsDir() && len(e.Name()) == 2 {
				dirs = append(dirs, filepath.Join(rel, e.Name()))
			}
		}
	}
	for _, d := range dirs {
		f, err := root.Open(d)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fsError(err, filepath.Join(p.Folder, d))
		}
		ents, err := f.ReadDir(-1)
		f.Close()
		if err != nil {
			return nil, fsError(err, filepath.Join(p.Folder, d))
		}
		for _, e := range ents {
			if !e.Type().IsRegular() || nodeproto.CheckBackupFile(p.Type, e.Name()) != nil {
				continue
			}
			if info, err := e.Info(); err == nil {
				out.Files = append(out.Files, nodeproto.BackupFile{Name: e.Name(), Size: info.Size()})
			}
		}
	}
	return out, nil
}

func backupRead(eff nodeproto.Capabilities, p nodeproto.BackupReadParams) (any, *nodeproto.Error) {
	root, rel, perr := openRepo(eff, p.BackupFileParams, false)
	if perr != nil {
		return nil, perr
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, fsError(err, filepath.Join(p.Folder, rel))
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%s is not a file", rel)
	}
	n := p.Length
	if n <= 0 || n > nodeproto.MaxBackupChunk {
		n = nodeproto.MaxBackupChunk
	}
	if p.Offset < 0 || p.Offset > st.Size() {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "offset %d outside a file of %d bytes", p.Offset, st.Size())
	}
	n = min(n, st.Size()-p.Offset)
	buf := make([]byte, n)
	got, err := f.ReadAt(buf, p.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fsError(err, filepath.Join(p.Folder, rel))
	}
	return nodeproto.BackupReadResult{Data: buf[:got], Size: st.Size()}, nil
}

func (a *Agent) backupWrite(eff nodeproto.Capabilities, p nodeproto.BackupWriteParams) (any, *nodeproto.Error) {
	if err := nodeproto.CheckUpload(p.Upload); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	if len(p.Data) > nodeproto.MaxBackupChunk {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "a chunk holds at most %d bytes", nodeproto.MaxBackupChunk)
	}
	root, rel, perr := openRepo(eff, p.BackupFileParams, false)
	if perr != nil {
		return nil, perr
	}
	defer root.Close()
	if err := root.MkdirAll(uploadsDir, 0o700); err != nil {
		return nil, fsError(err, p.Folder)
	}
	tmp := filepath.Join(uploadsDir, p.Upload)
	flags := os.O_RDWR
	if p.Offset == 0 {
		flags |= os.O_CREATE | os.O_TRUNC
	}
	f, err := root.OpenFile(tmp, flags, 0o600)
	if err != nil {
		return nil, fsError(err, filepath.Join(p.Folder, tmp))
	}
	fail := func(e *nodeproto.Error) (any, *nodeproto.Error) {
		f.Close()
		_ = root.Remove(tmp)
		return nil, e
	}
	if st, err := f.Stat(); err != nil || st.Size() != p.Offset {
		return fail(nodeproto.Errorf(nodeproto.CodeBadRequest, "upload %s: a chunk at %d out of order", p.Upload, p.Offset))
	}
	if _, err := f.WriteAt(p.Data, p.Offset); err != nil {
		return fail(fsError(err, filepath.Join(p.Folder, tmp)))
	}
	if !p.Final {
		if err := f.Close(); err != nil {
			return fail(fsError(err, filepath.Join(p.Folder, tmp)))
		}
		return nodeproto.BackupFile{Name: p.Name, Size: p.Offset + int64(len(p.Data))}, nil
	}
	if err := f.Sync(); err != nil {
		return fail(fsError(err, filepath.Join(p.Folder, tmp)))
	}
	size, sum, err := hashFile(f)
	if err != nil {
		return fail(fsError(err, filepath.Join(p.Folder, tmp)))
	}
	if size != p.Size || !strings.EqualFold(sum, p.SHA256) {
		return fail(nodeproto.Errorf(nodeproto.CodeFailed, "upload of %s: %d bytes with SHA-256 %s arrived, %d with %s were sent", p.Name, size, sum, p.Size, p.SHA256))
	}
	if err := f.Close(); err != nil {
		return fail(fsError(err, filepath.Join(p.Folder, tmp)))
	}
	// restic's files never change: one already there with this content
	// (a retry of a save that went through) is fine, another is not.
	if have, err := root.Open(rel); err == nil {
		_, hsum, herr := hashFile(have)
		have.Close()
		_ = root.Remove(tmp)
		if herr == nil && strings.EqualFold(hsum, sum) {
			return nodeproto.BackupFile{Name: p.Name, Size: size}, nil
		}
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "%s exists already with other content", rel)
	}
	if err := root.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
		_ = root.Remove(tmp)
		return nil, fsError(err, p.Folder)
	}
	if err := root.Rename(tmp, rel); err != nil {
		_ = root.Remove(tmp)
		return nil, fsError(err, filepath.Join(p.Folder, rel))
	}
	return nodeproto.BackupFile{Name: p.Name, Size: size}, nil
}

func hashFile(f *os.File) (int64, string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}

func (a *Agent) backupDelete(eff nodeproto.Capabilities, p nodeproto.BackupFileParams) (any, *nodeproto.Error) {
	if p.Type == nodeproto.BackupConfig {
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "a repository's config is never deleted over the node channel")
	}
	root, rel, perr := openRepo(eff, p, false)
	if perr != nil {
		return nil, perr
	}
	defer root.Close()
	if st, err := root.Lstat(rel); err != nil {
		return nil, fsError(err, filepath.Join(p.Folder, rel))
	} else if !st.Mode().IsRegular() {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%s is not a file", rel)
	}
	if err := root.Remove(rel); err != nil {
		return nil, fsError(err, filepath.Join(p.Folder, rel))
	}
	return struct{}{}, nil
}
