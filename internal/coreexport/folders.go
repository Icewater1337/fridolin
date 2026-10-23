package coreexport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/vault"
)

// Brain folders outside the data dir (bound into the container from the
// machine's home, ~/Personal) go along with an export, as far as the
// brain's backup settings let them reach the cloud: the export goes
// through the vault bucket. They travel under folders/<brain id>/ in the
// archive. The import puts a folder back where the brain's path names it
// when that folder is there (the way back home), and otherwise into the
// volume, /data/brains-folders/<name>, pointing the brain there and
// keeping its home path in brain_dirs' moved_from (docs/CORE.md, "Brain
// folders on another machine").

// foldersPrefix is where brain folders lie in the archive.
const foldersPrefix = "folders/"

// formatFolders is the archive format of an export with brain folders: an
// older import, which knows only data/ members, refuses it.
const formatFolders = 2

// Folder is a brain folder outside the data dir that an export takes
// along.
type Folder struct {
	ID string
	// Configured is the folder as brain_dirs has it (~/Personal), Dir the
	// folder it is on this machine.
	Configured, Dir string
	// Skip says why the folder stays where it is (its backup is off, its
	// brain.json cannot be read); "" takes it along.
	Skip string
	// Excluded reports whether a path below Dir, slash-separated, stays:
	// what the brain's backup settings leave out of the cloud. nil keeps
	// nothing back.
	Excluded func(rel string) bool
	// Visiting says the folder lies in the data dir because a core move
	// put it there (its brain_dirs entry has moved_from). The data dir's
	// export then leaves out of it what Excluded and folderJunk leave out
	// on the way out, so the way back carries no more through the cloud
	// than the way there did.
	Visiting bool
}

// FolderInfo is a brain folder in the manifest.
type FolderInfo struct {
	ID         string `json:"id"`
	Configured string `json:"configured"`
	Dir        string `json:"dir"`
	Files      int    `json:"files"`
	Bytes      int64  `json:"bytes"`
	// Left counts the files and folders that stayed: what the brain's
	// backup settings leave out, virtualenvs and node_modules, links.
	Left int `json:"left,omitempty"`
}

// validFolderID is a brain id as a member name may carry it.
var validFolderID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// folderJunk reports whether a folder below a brain folder stays home
// whatever the brain's settings: programs built for the machine they were
// made on (docs/CORE.md, "What does not move").
func folderJunk(abs, base string) bool {
	if base == "node_modules" || base == "__pycache__" {
		return true
	}
	_, err := os.Stat(filepath.Join(abs, "pyvenv.cfg"))
	return err == nil
}

// folderItems lists what of f goes into the archive, as items under
// folders/<id>/, and counts what stays.
func folderItems(f Folder, logf func(string, ...any)) ([]item, FolderInfo, error) {
	info := FolderInfo{ID: f.ID, Configured: f.Configured, Dir: f.Dir}
	var items []item
	root := filepath.Clean(f.Dir)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if (f.Excluded != nil && f.Excluded(rel)) || (d.IsDir() && folderJunk(p, d.Name())) {
			info.Left++
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		name := foldersPrefix + f.ID + "/" + rel
		switch {
		case d.IsDir():
			items = append(items, item{rel: name, src: p, dir: true, mode: st.Mode().Perm(), mtime: st.ModTime()})
		case st.Mode().IsRegular():
			items = append(items, item{rel: name, src: p, mode: st.Mode().Perm(), mtime: st.ModTime()})
			info.Files++
			info.Bytes += st.Size()
		default:
			info.Left++
			logf("warning: %s/%s is not a regular file or folder (%s): it stays", f.Dir, rel, st.Mode().Type())
		}
		return nil
	})
	return items, info, err
}

// cleanFolderMember checks a folders/ member's name and returns the brain
// id and the path below its folder ("" for the folder itself).
func cleanFolderMember(name string) (id, rel string, err error) {
	rest, ok := strings.CutPrefix(name, foldersPrefix)
	rest = strings.TrimSuffix(rest, "/")
	bad := fmt.Errorf("unsafe member %q in the archive", name)
	if !ok || rest == "" || strings.Contains(rest, "\\") || path.Clean(rest) != rest || path.IsAbs(rest) {
		return "", "", bad
	}
	id, rel, _ = strings.Cut(rest, "/")
	if !validFolderID.MatchString(id) {
		return "", "", bad
	}
	for _, part := range strings.Split(rest, "/") {
		if part == ".." || part == "." || part == "" {
			return "", "", bad
		}
	}
	return id, rel, nil
}

// stagedFolders is where Import stages the brain folders: in the staging
// folder's core/, which Activate leaves where it is.
func stagedFolders(staging string) string { return filepath.Join(staging, "core", "folders") }

// FolderPlacement says where PlaceFolders put one brain's folder.
type FolderPlacement struct {
	ID string
	// Dir is where the brain's folder is now.
	Dir string
	// Home is true when the folder went back to its home machine's folder,
	// with Updated files written over (each kept in Aside first), Added new
	// ones and Same left as they were; false when it went into the volume.
	Home                 bool
	Added, Updated, Same int
	Aside                string
	// Conflicts are the files (slash-separated, below the folder) that
	// changed both at home and in the brain's copy since the move: the
	// home version stays, the brain's is in Aside. Kept counts the files
	// that changed only at home, which stay as they are.
	Conflicts []string
	Kept      int
	// Forget is the vault path of the data dir's copy that came home
	// (brains-folders/Personal): the vault forgets it when it next starts
	// (vault.ForgetNoteName).
	Forget string
}

// PlaceFolders puts the brain folders of an import in place, after
// Activate: those the archive carries (staged by Import) and those that
// came back inside the data dir (a brain_dirs entry with moved_from). A
// folder whose home (the brain's path, or moved_from, with home for ~/) is
// there outside the data dir gets the brain's changes merged into it; one
// whose home is not there goes into dataDir/brains-folders/, and the brain
// points there. Nothing at home is deleted: a file it replaces, and the
// volume's copy that came home, go to core/replaced-<time>/folders/ in the
// data dir. The vault is told to forget the data dir's copy of a folder
// that came home: what it uploaded of it while the folder was away is no
// longer anyone's (a note in core/, vault.ForgetNoteName).
func PlaceFolders(dataDir, staging, home string, m Manifest, now time.Time) ([]FolderPlacement, error) {
	cfgPath := filepath.Join(dataDir, "config.json")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && len(m.Folders) == 0 {
			return nil, nil
		}
		return nil, err
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", cfgPath, err)
	}
	var entries []map[string]json.RawMessage
	if raw, ok := cfg["brain_dirs"]; ok {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, fmt.Errorf("%s: brain_dirs: %w", cfgPath, err)
		}
	}
	str := func(e map[string]json.RawMessage, k string) string {
		var s string
		_ = json.Unmarshal(e[k], &s)
		return s
	}
	idOf := func(e map[string]json.RawMessage) string {
		var a struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(e["approved"], &a)
		return a.ID
	}
	expand := func(p string) string {
		if rest, ok := strings.CutPrefix(p, "~/"); ok && home != "" {
			return filepath.Join(home, rest)
		}
		return p
	}
	isHome := func(dir string) bool {
		st, err := os.Stat(dir)
		return err == nil && st.IsDir() && filepath.IsAbs(dir) && !within(dataDir, dir)
	}
	stamp := now.UTC().Format("20060102T150405Z")
	asideRoot := filepath.Join(dataDir, "core", "replaced-"+stamp, "folders")
	var out []FolderPlacement
	var forget []vault.ForgetFolder
	changed := false

	// The folders the archive carries.
	for _, f := range m.Folders {
		src := filepath.Join(stagedFolders(staging), f.ID)
		if _, err := os.Stat(src); err != nil {
			return out, fmt.Errorf("the brain folder of %s is not in the staged import: %w", f.ID, err)
		}
		var entry map[string]json.RawMessage
		for _, e := range entries {
			if idOf(e) == f.ID {
				entry = e
			}
		}
		if dir := expand(f.Configured); isHome(dir) {
			p, err := mergeHome(dir, src, filepath.Join(asideRoot, f.ID), nil)
			if err != nil {
				return out, fmt.Errorf("bringing %s's folder home to %s: %w", f.ID, dir, err)
			}
			p.ID = f.ID
			out = append(out, p)
			continue
		}
		target := filepath.Join(dataDir, "brains-folders", filepath.Base(f.Configured))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return out, err
		}
		if _, err := os.Lstat(target); err == nil {
			if err := moveAside(target, filepath.Join(asideRoot, "volume", filepath.Base(target))); err != nil {
				return out, err
			}
		}
		if err := os.Rename(src, target); err != nil {
			return out, fmt.Errorf("putting %s's folder into %s: %w", f.ID, target, err)
		}
		if err := writeOrigin(target, f.ID, now); err != nil {
			return out, fmt.Errorf("recording what %s's folder held when it moved: %w", f.ID, err)
		}
		out = append(out, FolderPlacement{ID: f.ID, Dir: target})
		if entry != nil {
			entry["path"], _ = json.Marshal(target)
			if str(entry, "moved_from") == "" {
				entry["moved_from"], _ = json.Marshal(f.Configured)
			}
			changed = true
		}
	}

	// The folders that went away in an earlier move and come back now.
	for _, e := range entries {
		from, here := str(e, "moved_from"), str(e, "path")
		// The path names the volume of the machine the export came from:
		// the same folder below this data dir.
		if m.DataDir != "" && within(m.DataDir, here) && !within(dataDir, here) {
			if rel, err := filepath.Rel(m.DataDir, here); err == nil {
				here = filepath.Join(dataDir, rel)
			}
		}
		if from == "" || !within(dataDir, here) {
			continue
		}
		dir := expand(from)
		if !isHome(dir) {
			continue // still away from home: it stays in the volume
		}
		id := idOf(e)
		if id == "" {
			id = filepath.Base(here)
		}
		if _, err := os.Stat(here); err != nil {
			return out, fmt.Errorf("the brain folder of %s, %s, is not in the import: %w", id, here, err)
		}
		origin, err := readOrigin(here)
		if err != nil {
			return out, err
		}
		p, err := mergeHome(dir, here, filepath.Join(asideRoot, id), origin)
		if err != nil {
			return out, fmt.Errorf("bringing %s's folder home to %s: %w", id, dir, err)
		}
		if err := moveAside(here, filepath.Join(asideRoot, "volume", filepath.Base(here))); err != nil {
			return out, err
		}
		if origin != nil {
			if err := moveAside(originFile(here), filepath.Join(asideRoot, "volume", filepath.Base(originFile(here)))); err != nil {
				return out, err
			}
		}
		p.ID = id
		if rel, err := filepath.Rel(dataDir, here); err == nil && rel != "." {
			p.Forget = filepath.ToSlash(rel)
			forget = append(forget, vault.ForgetFolder{Prefix: p.Forget, Why: "brain " + id + "'s folder went home to " + from, At: now.UTC()})
		}
		out = append(out, p)
		e["path"], _ = json.Marshal(from)
		delete(e, "moved_from")
		changed = true
	}
	if len(forget) > 0 {
		note := filepath.Join(dataDir, "core", vault.ForgetNoteName)
		if err := vault.AddToForgetNote(note, forget...); err != nil {
			return out, fmt.Errorf("noting for the vault what to forget in %s: %w (fridolin vault forget does it by hand)", note, err)
		}
	}
	if !changed {
		return out, nil
	}
	cfg["brain_dirs"], err = json.Marshal(entries)
	if err != nil {
		return out, err
	}
	buf, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return out, err
	}
	tmp := cfgPath + ".import.tmp"
	if err := os.WriteFile(tmp, append(buf, '\n'), 0o600); err != nil {
		return out, err
	}
	return out, os.Rename(tmp, cfgPath)
}

// originPrefix starts the name of the file beside a folder in the volume
// that records what the folder held when the move put it there: the
// sha256 of each file. It lies in brains-folders/, outside the folder
// (the brain never sees it, and it never goes home), travels with the
// export as part of the data dir and is no vault file.
const originPrefix = ".moved-"

// folderOrigin is the content of that file.
type folderOrigin struct {
	ID    string            `json:"id"`
	At    time.Time         `json:"at"`
	Files map[string]string `json:"files"` // slash-separated path below the folder → sha256, hex
}

func originFile(dir string) string {
	return filepath.Join(filepath.Dir(dir), originPrefix+filepath.Base(dir)+".json")
}

// writeOrigin records the sha256 of every regular file below dir.
func writeOrigin(dir, id string, now time.Time) error {
	o := folderOrigin{ID: id, At: now.UTC(), Files: map[string]string{}}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		h, err := fileHash(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		o.Files[filepath.ToSlash(rel)] = hex.EncodeToString(h)
		return nil
	})
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	tmp := originFile(dir) + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, originFile(dir))
}

// readOrigin returns the hashes writeOrigin recorded for dir, nil when
// there are none (a move made before they were recorded).
func readOrigin(dir string) (map[string]string, error) {
	data, err := os.ReadFile(originFile(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var o folderOrigin
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("%s: %w", originFile(dir), err)
	}
	if o.Files == nil {
		o.Files = map[string]string{}
	}
	return o.Files, nil
}

// mergeHome writes src's files into dst, the brain's folder at home: new
// ones are added, changed ones written over after the file there moved to
// aside, the same ones left alone. Nothing in dst is deleted, and nothing
// is written through a link in dst. With origin, the files' hashes when
// the folder left home, a file that changed at home since stays: as it is
// when the brain's copy did not change it, and with the brain's version
// in aside, a conflict, when it did. Without origin the brain's copy wins.
func mergeHome(dst, src, aside string, origin map[string]string) (FolderPlacement, error) {
	p := FolderPlacement{Dir: dst, Home: true}
	err := filepath.WalkDir(src, func(sp string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, sp)
		if err != nil || rel == "." {
			return err
		}
		dp := filepath.Join(dst, rel)
		if err := noLinkBelow(dst, dp); err != nil {
			return err
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			if fi, err := os.Lstat(dp); err == nil && !fi.IsDir() {
				return fmt.Errorf("%s is a folder in the brain's copy and a file at home", rel)
			}
			return os.MkdirAll(dp, st.Mode().Perm()|0o700)
		}
		if !st.Mode().IsRegular() {
			return nil
		}
		if fi, err := os.Lstat(dp); err == nil {
			if !fi.Mode().IsRegular() {
				return fmt.Errorf("%s is a file in the brain's copy and no regular file at home", rel)
			}
			same, err := sameFile(sp, dp)
			if err != nil {
				return err
			}
			if same {
				p.Same++
				return nil
			}
			if origin != nil {
				slash := filepath.ToSlash(rel)
				was, known := origin[slash]
				home, err := fileHash(dp)
				if err != nil {
					return err
				}
				if !known || hex.EncodeToString(home) != was {
					// Changed at home since the move (or made there
					// meanwhile): home wins.
					moved, err := fileHash(sp)
					if err != nil {
						return err
					}
					if known && hex.EncodeToString(moved) == was {
						p.Kept++
						return nil
					}
					p.Conflicts = append(p.Conflicts, slash)
					p.Aside = aside
					if err := os.MkdirAll(filepath.Dir(filepath.Join(aside, rel)), 0o700); err != nil {
						return err
					}
					return copyFile(sp, filepath.Join(aside, rel), st.Mode().Perm(), st.ModTime())
				}
			}
			if err := moveAside(dp, filepath.Join(aside, rel)); err != nil {
				return err
			}
			p.Updated++
			p.Aside = aside
		} else if errors.Is(err, fs.ErrNotExist) {
			p.Added++
		} else {
			return err
		}
		return copyFile(sp, dp, st.Mode().Perm(), st.ModTime())
	})
	return p, err
}

// noLinkBelow refuses p when a folder between root and p is a link, so a
// merge never writes outside the folder it brings home.
func noLinkBelow(root, p string) error {
	rel, err := filepath.Rel(root, filepath.Dir(p))
	if err != nil || rel == "." {
		return err
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a link: nothing is written below it", cur)
		}
	}
	return nil
}

func sameFile(a, b string) (bool, error) {
	sa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	if sa.Size() != sb.Size() {
		return false, nil
	}
	ha, err := fileHash(a)
	if err != nil {
		return false, err
	}
	hb, err := fileHash(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(ha, hb), nil
}

func fileHash(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// copyFile writes src to dst through a temporary file beside dst.
func copyFile(src, dst string, mode fs.FileMode, mtime time.Time) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".fridolin-import-*")
	if err != nil {
		return err
	}
	_, err = io.Copy(tmp, in)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), mode|0o600)
	}
	if err == nil {
		_ = os.Chtimes(tmp.Name(), mtime, mtime)
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// moveAside moves p to dst, making dst's folder; across disks (a folder
// bound in from the host and the volume) it copies and then removes.
func moveAside(p, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Rename(p, dst); err == nil {
		return nil
	}
	if err := copyTree(p, dst); err != nil {
		return fmt.Errorf("keeping %s in %s: %w", p, dst, err)
	}
	return os.RemoveAll(p)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		st, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, st.Mode().Perm()|0o700)
		case st.Mode().IsRegular():
			return copyFile(p, target, st.Mode().Perm(), st.ModTime())
		default:
			return nil // a link stays behind; the original goes only after this
		}
	})
}

// ListFolders reports what an export would take along of folders, with
// nothing read but the folders and nothing uploaded: emit gets each file
// as <brain id>/<path below its folder>. A folder that stays (Skip, or one
// below dataDir, which travels as part of the data dir) is left out.
func ListFolders(dataDir string, folders []Folder, emit func(string), logf func(string, ...any)) ([]FolderInfo, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var out []FolderInfo
	for _, f := range folders {
		if f.Skip != "" {
			logf("brain folder %s stays: %s", f.Dir, f.Skip)
			continue
		}
		if within(dataDir, f.Dir) {
			continue
		}
		its, info, err := folderItems(f, logf)
		if err != nil {
			return out, fmt.Errorf("the brain folder %s: %w", f.Dir, err)
		}
		for _, it := range its {
			if !it.dir {
				emit(strings.TrimPrefix(it.rel, foldersPrefix))
			}
		}
		out = append(out, info)
	}
	return out, nil
}
