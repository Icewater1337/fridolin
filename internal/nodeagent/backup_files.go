package nodeagent

import (
	"crypto/sha256"
	"encoding/hex"
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

// The backup family's folder listing and repository files. Every path is
// confined in backup_paths.go before it reaches a filesystem call.

// systemFolders are folders a picker leaves out on Windows.
var systemFolders = []string{"$recycle.bin", "system volume information", "$windows.~bt", "$windows.~ws", "recovery"}

// browse lists the names of the folders in p: never a file.
func browse(eff nodeproto.Capabilities, p nodeproto.BrowseParams) (any, *nodeproto.Error) {
	dir, perr := confinedBrowse(eff, p.Path)
	if perr != nil {
		return nil, perr
	}
	clean := filepath.Clean(p.Path)
	if root, _ := lentRoot(eff, dir); root != "" && !inside(root, dir) {
		return nil, nodeproto.Errorf(nodeproto.CodeOutsideRoot, "%s leads out of the places this node lends for backups", clean)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fsError(err, clean)
	}
	out := nodeproto.BrowseResult{Path: clean, Folders: []string{}}
	if parent := filepath.Dir(clean); parent != clean && eff.AllowsBrowse(parent) {
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
	if root, err := os.OpenRoot(dir); err == nil {
		out.Repo = isRepo(root)
		root.Close()
	}
	return out, nil
}

// isRepo reports whether root holds a restic repository: its config file
// and its keys folder.
func isRepo(root *os.Root) bool {
	c, err := root.Lstat(nodeproto.BackupConfig)
	k, kerr := root.Lstat(nodeproto.BackupKeys)
	return err == nil && c.Mode().IsRegular() && kerr == nil && k.IsDir()
}

func (a *Agent) backupCreate(eff nodeproto.Capabilities, requested string) (any, *nodeproto.Error) {
	dir, perr := confinedFolder(eff, requested)
	if perr != nil {
		return nil, perr
	}
	if root, ok := lentRoot(eff, dir); !ok || root != "" && !inside(root, dir) {
		return nil, nodeproto.Errorf(nodeproto.CodeOutsideRoot, "%s leads out of the places this node lends for backups", requested)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fsError(err, requested)
	}
	r, perr := openFolder(eff, requested)
	if perr != nil {
		return nil, perr
	}
	defer r.close()
	dirs := []string{nodeproto.BackupKeys, nodeproto.BackupLocks, nodeproto.BackupSnapshots, nodeproto.BackupIndex, nodeproto.BackupData, uploadsDir}
	for i := range 256 {
		dirs = append(dirs, filepath.Join(nodeproto.BackupData, hex.EncodeToString([]byte{byte(i)})))
	}
	for _, d := range dirs {
		rel, perr := r.at(d)
		if perr != nil {
			return nil, perr
		}
		if err := r.root.MkdirAll(rel, 0o700); err != nil {
			return nil, fsError(err, filepath.Join(requested, d))
		}
	}
	pruneUploads(r.root, time.Now())
	a.logf("node: made the backup folder %s", requested)
	return folderInfo(r, requested), nil
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
		rel, err := uploadRel(e.Name())
		if err != nil {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > staleUpload {
			_ = root.Remove(rel)
		}
	}
}

func backupInfo(eff nodeproto.Capabilities, requested string) (any, *nodeproto.Error) {
	r, perr := openFolder(eff, requested)
	if perr != nil {
		if perr.Code == nodeproto.CodeNotFound {
			return nodeproto.BackupInfo{Folder: requested}, nil
		}
		return nil, perr
	}
	defer r.close()
	return folderInfo(r, requested), nil
}

// folderInfo is the open folder's state.
func folderInfo(r repo, folder string) nodeproto.BackupInfo {
	out := nodeproto.BackupInfo{Folder: folder, Exists: true, Repo: isRepo(r.root)}
	if free, err := platform.FreeBytes(r.dir); err == nil {
		out.Free = free
	}
	_ = fs.WalkDir(r.root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
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
	return out
}

// openFile opens the folder of a file request and gives the file's path
// in it, checked.
func openFile(eff nodeproto.Capabilities, p nodeproto.BackupFileParams) (repo, string, *nodeproto.Error) {
	rel, err := fileRel(p.Type, p.Name)
	if err != nil {
		return repo{}, "", nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	r, perr := openFolder(eff, p.Folder)
	if perr != nil {
		return repo{}, "", perr
	}
	if rel, perr = r.at(rel); perr != nil {
		r.close()
		return repo{}, "", perr
	}
	return r, rel, nil
}

func backupStat(eff nodeproto.Capabilities, p nodeproto.BackupFileParams) (any, *nodeproto.Error) {
	r, rel, perr := openFile(eff, p)
	if perr != nil {
		return nil, perr
	}
	defer r.close()
	st, err := r.root.Lstat(rel)
	if err != nil {
		return nil, fsError(err, rel)
	}
	if !st.Mode().IsRegular() {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%s is not a file", rel)
	}
	return nodeproto.BackupFile{Name: p.Name, Size: st.Size()}, nil
}

func backupList(eff nodeproto.Capabilities, p nodeproto.BackupFileParams) (any, *nodeproto.Error) {
	dir, ok := typeDir(p.Type)
	if !ok || p.Name != "" {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "backup: cannot list %q", p.Type)
	}
	r, perr := openFolder(eff, p.Folder)
	if perr != nil {
		return nil, perr
	}
	defer r.close()
	out := nodeproto.BackupListResult{Files: []nodeproto.BackupFile{}}
	dirs := []string{dir}
	if dir == nodeproto.BackupData {
		dirs = nil
		for i := range 256 {
			dirs = append(dirs, filepath.Join(nodeproto.BackupData, hex.EncodeToString([]byte{byte(i)})))
		}
	}
	for _, d := range dirs {
		rel, perr := r.at(d)
		if perr != nil {
			return nil, perr
		}
		f, err := r.root.Open(rel)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fsError(err, d)
		}
		ents, err := f.ReadDir(-1)
		f.Close()
		if err != nil {
			return nil, fsError(err, d)
		}
		for _, e := range ents {
			if !e.Type().IsRegular() || nodeproto.CheckBackupFile(p.Type, e.Name()) != nil {
				continue
			}
			// A data file lies in the folder of its first two digits only.
			if dir == nodeproto.BackupData && !strings.HasPrefix(e.Name(), filepath.Base(d)) {
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
	r, rel, perr := openFile(eff, p.BackupFileParams)
	if perr != nil {
		return nil, perr
	}
	defer r.close()
	f, err := r.root.Open(rel)
	if err != nil {
		return nil, fsError(err, rel)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%s is not a file", rel)
	}
	if p.Offset < 0 || p.Offset > st.Size() {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "offset %d outside a file of %d bytes", p.Offset, st.Size())
	}
	n := int64(nodeproto.MaxBackupChunk)
	if p.Length > 0 && p.Length < n {
		n = p.Length
	}
	n = min(n, st.Size()-p.Offset)
	// A buffer of the fixed chunk size, of which n bytes are read.
	buf := make([]byte, nodeproto.MaxBackupChunk)
	got, err := f.ReadAt(buf[:n], p.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fsError(err, rel)
	}
	return nodeproto.BackupReadResult{Data: buf[:got], Size: st.Size()}, nil
}

func (a *Agent) backupWrite(eff nodeproto.Capabilities, p nodeproto.BackupWriteParams) (any, *nodeproto.Error) {
	tmp, err := uploadRel(p.Upload)
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	if len(p.Data) > nodeproto.MaxBackupChunk {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "a chunk holds at most %d bytes", nodeproto.MaxBackupChunk)
	}
	r, rel, perr := openFile(eff, p.BackupFileParams)
	if perr != nil {
		return nil, perr
	}
	defer r.close()
	if tmp, perr = r.at(tmp); perr != nil {
		return nil, perr
	}
	if err := r.root.MkdirAll(uploadsDir, 0o700); err != nil {
		return nil, fsError(err, uploadsDir)
	}
	flags := os.O_RDWR
	if p.Offset == 0 {
		flags |= os.O_CREATE | os.O_TRUNC
	}
	f, err := r.root.OpenFile(tmp, flags, 0o600)
	if err != nil {
		return nil, fsError(err, tmp)
	}
	fail := func(e *nodeproto.Error) (any, *nodeproto.Error) {
		f.Close()
		_ = r.root.Remove(tmp)
		return nil, e
	}
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() || st.Size() != p.Offset {
		return fail(nodeproto.Errorf(nodeproto.CodeBadRequest, "upload %s: a chunk at %d out of order", p.Upload, p.Offset))
	}
	if _, err := f.WriteAt(p.Data, p.Offset); err != nil {
		return fail(fsError(err, tmp))
	}
	if !p.Final {
		if err := f.Close(); err != nil {
			return fail(fsError(err, tmp))
		}
		return nodeproto.BackupFile{Name: p.Name, Size: p.Offset + int64(len(p.Data))}, nil
	}
	if err := f.Sync(); err != nil {
		return fail(fsError(err, tmp))
	}
	size, sum, err := hashFile(f)
	if err != nil {
		return fail(fsError(err, tmp))
	}
	if size != p.Size || !strings.EqualFold(sum, p.SHA256) {
		return fail(nodeproto.Errorf(nodeproto.CodeFailed, "upload of %s: %d bytes with SHA-256 %s arrived, %d with %s were sent", p.Name, size, sum, p.Size, p.SHA256))
	}
	if err := f.Close(); err != nil {
		return fail(fsError(err, tmp))
	}
	// restic's files never change: one already there with this content
	// (a retry of a save that went through) is fine, another is not.
	if have, err := r.root.Open(rel); err == nil {
		_, hsum, herr := hashFile(have)
		have.Close()
		_ = r.root.Remove(tmp)
		if herr == nil && strings.EqualFold(hsum, sum) {
			return nodeproto.BackupFile{Name: p.Name, Size: size}, nil
		}
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "%s exists already with other content", rel)
	}
	parent, perr := r.at(filepath.Dir(rel))
	if perr != nil {
		_ = r.root.Remove(tmp)
		return nil, perr
	}
	if err := r.root.MkdirAll(parent, 0o700); err != nil {
		_ = r.root.Remove(tmp)
		return nil, fsError(err, parent)
	}
	if err := r.root.Rename(tmp, rel); err != nil {
		_ = r.root.Remove(tmp)
		return nil, fsError(err, rel)
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
	r, rel, perr := openFile(eff, p)
	if perr != nil {
		return nil, perr
	}
	defer r.close()
	if st, err := r.root.Lstat(rel); err != nil {
		return nil, fsError(err, rel)
	} else if !st.Mode().IsRegular() {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%s is not a file", rel)
	}
	if err := r.root.Remove(rel); err != nil {
		return nil, fsError(err, rel)
	}
	return struct{}{}, nil
}
