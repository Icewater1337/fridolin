package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"
)

// coldListingName is the file in each folder with cold members that lists
// them, so a plain ls still shows what exists.
const coldListingName = ".cold.json"

// ColdListing is the content of a .cold.json.
type ColdListing struct {
	Note  string     `json:"note"`
	Files []ColdFile `json:"files"`
}

// ColdFile is one cold member of a folder.
type ColdFile struct {
	Name   string    `json:"name"`
	Size   int64     `json:"size"`
	MTime  time.Time `json:"mtime"`
	SHA256 string    `json:"sha256"`
}

const coldListingNote = "These files are in the vault's cold tier, not on disk. vault_get brings one back."

func coldFileOf(r row) ColdFile {
	return ColdFile{Name: path.Base(r.Path), Size: r.Size, MTime: time.Unix(0, r.MtimeNS).UTC(), SHA256: r.ColdSHA256}
}

func (v *Vault) coldListingPath(dir string) string {
	return filepath.Join(v.opts.Root, filepath.FromSlash(dir), coldListingName)
}

// ReadColdListing reads the .cold.json of dir ("" for the root); a folder
// without one has an empty listing.
func (v *Vault) ReadColdListing(dir string) (ColdListing, error) {
	data, err := v.root.ReadFile(filepath.Join(filepath.FromSlash(dir), coldListingName))
	if errors.Is(err, fs.ErrNotExist) {
		return ColdListing{}, nil
	}
	if err != nil {
		return ColdListing{}, err
	}
	var l ColdListing
	if err := json.Unmarshal(data, &l); err != nil {
		return ColdListing{}, fmt.Errorf("vault: %s: %w", v.coldListingPath(dir), err)
	}
	return l, nil
}

// writeColdListing writes dir's listing, or removes it when empty.
func (v *Vault) writeColdListing(dir string, files []ColdFile) error {
	if len(files) == 0 {
		err := v.root.Remove(filepath.Join(filepath.FromSlash(dir), coldListingName))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	data, err := json.MarshalIndent(ColdListing{Note: coldListingNote, Files: files}, "", "  ")
	if err != nil {
		return err
	}
	if dir != "" {
		if err := v.root.MkdirAll(filepath.FromSlash(dir), 0o700); err != nil {
			return err
		}
	}
	return v.writeFileAtomic(path.Join(dir, coldListingName), append(data, '\n'))
}

// addColdListing adds (or updates) r in its folder's .cold.json.
func (v *Vault) addColdListing(r row) error {
	dir := dirOf(r.Path)
	l, err := v.ReadColdListing(dir)
	if err != nil {
		return err
	}
	entry := coldFileOf(r)
	files := l.Files[:0:0]
	for _, f := range l.Files {
		if f.Name != entry.Name {
			files = append(files, f)
		}
	}
	return v.writeColdListing(dir, append(files, entry))
}

// removeColdListing drops rel from its folder's .cold.json.
func (v *Vault) removeColdListing(rel string) error {
	dir := dirOf(rel)
	l, err := v.ReadColdListing(dir)
	if err != nil {
		return err
	}
	name := path.Base(rel)
	files := l.Files[:0:0]
	for _, f := range l.Files {
		if f.Name != name {
			files = append(files, f)
		}
	}
	if len(files) == len(l.Files) {
		return nil
	}
	return v.writeColdListing(dir, files)
}

// rewriteColdListings makes every .cold.json match the index: one per
// folder with tier-cold rows, none elsewhere.
func (v *Vault) rewriteColdListings(rows []row) error {
	want := map[string][]ColdFile{}
	for _, r := range rows {
		if r.Tier == TierCold {
			want[dirOf(r.Path)] = append(want[dirOf(r.Path)], coldFileOf(r))
		}
	}
	var stale []string
	err := fs.WalkDir(v.root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != "." && IgnoredName(d.Name()) {
			return fs.SkipDir
		}
		if !d.IsDir() && d.Name() == coldListingName {
			dir := filepath.ToSlash(filepath.Dir(p))
			if dir == "." {
				dir = ""
			}
			if _, ok := want[dir]; !ok {
				stale = append(stale, dir)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, dir := range stale {
		if err := v.writeColdListing(dir, nil); err != nil {
			return err
		}
	}
	for dir, files := range want {
		if err := v.writeColdListing(dir, files); err != nil {
			return err
		}
	}
	return nil
}

// writeFileAtomic writes data to rel through the root (so no symlink can
// lead it outside) by way of a temp file and a rename.
func (v *Vault) writeFileAtomic(rel string, data []byte) error {
	tmp := tempName(rel, "write")
	f, err := v.root.OpenFile(filepath.FromSlash(tmp), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = v.root.Rename(filepath.FromSlash(tmp), filepath.FromSlash(rel))
	}
	if err != nil {
		_ = v.root.Remove(filepath.FromSlash(tmp))
	}
	return err
}
