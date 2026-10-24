// Package coreexport moves the core's hot tier between machines: Export
// writes the data dir as one age-encrypted archive to the vault bucket,
// Import fetches, decrypts and checks one into a staging folder, and
// Activate puts a staged copy in place of a data dir that no bridge runs on
// (docs/CORE.md).
//
// The archive is a gzip-compressed tar, sealed (internal/seal) under the
// export key and then encrypted with age to the core's identity: manifest.json first, then the data
// dir's files under data/. SQLite databases are copied with VACUUM INTO,
// never as files, so a live bridge's bridge.db is exported consistently.
// What any machine rebuilds or the cold tier holds completely is left out:
// runs/, run-tokens/, media/ (unless asked for), vault/, core/ (the lease's
// device id and the export's own staging), migration/ (a native install's
// own snapshots from earlier moves), lock files, SQLite's sidecars, the brains' tmp/ and
// cache/ folders, node_modules/ and *.tmp files.
package coreexport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/seal"
)

// nopCloser leaves closing to the layer below.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// Store is the vault bucket exports go to. Export and import use Get and
// Put; Get of a missing key returns an error that matches fs.ErrNotExist,
// as cloud.ErrNotFound does.
type Store = cloud.ObjectStore

// Keys in the vault bucket.
const (
	ExportsPrefix = "core/exports/"
	LatestKey     = ExportsPrefix + "latest"
	keySuffix     = ".tar.gz.age"
	manifestName  = "manifest.json"
	dataPrefix    = "data/"
	// FormatVersion is the newest archive format: 1, or 2 when it carries
	// brain folders (formatFolders). Import refuses a newer one.
	FormatVersion = formatFolders
)

// Manifest describes an export. It is the archive's first member.
type Manifest struct {
	Format    int       `json:"format"`
	Version   string    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	Hostname  string    `json:"hostname,omitempty"`
	// DataDir is the data dir the export was made from.
	DataDir string `json:"data_dir"`
	// Files and Bytes count the data dir's files in the archive.
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
	// Databases are the SQLite databases in it, by their path below the
	// data dir, each copied with VACUUM INTO.
	Databases []string `json:"databases"`
	// OutsideBrainFolders are brain folders that are not below the data
	// dir and so are not in the export: they move separately.
	OutsideBrainFolders []string `json:"outside_brain_folders,omitempty"`
	// Skipped are files that are neither regular files nor folders
	// (symbolic links, sockets), which an export leaves out.
	Skipped []string `json:"skipped,omitempty"`
	// Folders are the brain folders from outside the data dir that the
	// archive carries under folders/ (format 2), each with its own counts.
	Folders []FolderInfo `json:"folders,omitempty"`
	// DeviceID is the exported data dir's device id (the lease's), and
	// Move the core move it was made for (#187), when one.
	DeviceID string `json:"device_id,omitempty"`
	Move     string `json:"move,omitempty"`
}

// DB copies and checks SQLite databases; the store package's VacuumInto
// and IntegrityCheck in production.
type DB interface {
	VacuumInto(ctx context.Context, src, dst string) error
	IntegrityCheck(ctx context.Context, path string) error
}

// ExportOptions are Export's settings besides the data dir, recipients and
// store.
type ExportOptions struct {
	DB       DB
	Version  string
	Hostname string
	// BrainFolders are the brain folders of the config, absolute: those
	// below the data dir are in the export anyway, the others are listed in
	// the manifest and warned about, unless Folders takes them along.
	BrainFolders []string
	// Folders are brain folders outside the data dir to take along.
	Folders []Folder
	// TempDir holds the database snapshots while the archive is written;
	// default <dataDir>/core/export-tmp, on the same disk as the data. An
	// export of a data dir that must stay as it is (a native install's,
	// read while it moves into the container) names a folder elsewhere.
	TempDir string
	// IncludeMedia exports media/ as well, which a core leaves out because
	// its vault holds it: a native install without the vault has the only
	// copy of its inboxes and recordings there.
	IncludeMedia bool
	// SealKey, when set, seals the archive (internal/seal) under it before
	// age encrypts it: two layers with independent keys, the outer one
	// post-quantum when the recipients are (docs/CORE.md, "Encryption").
	SealKey []byte
	// DeviceID and Move go into the manifest and the export's info
	// (Info), which a core move reads without the keys.
	DeviceID, Move string
	Now            func() time.Time
	Logf           func(format string, args ...any)
}

// ExportResult is what Export wrote.
type ExportResult struct {
	Key      string
	Manifest Manifest
	Info     Info
}

// item is one member of the archive: rel is its name below data/, or its
// whole name below folders/.
type item struct {
	rel, src string
	dir      bool
	mode     fs.FileMode
	mtime    time.Time
}

// excluded reports whether rel, a slash path below the data dir, stays out
// of an export, and whether it is a folder.
func excluded(rel string, dir bool) bool {
	parts := strings.Split(rel, "/")
	base := parts[len(parts)-1]
	if dir {
		if len(parts) == 1 && slices.Contains([]string{"runs", "run-tokens", "media", "vault", "core", "migration"}, base) {
			return true
		}
		// A brain's TMPDIR and caches (brains/<id>/tmp, brains/<id>/cache).
		if len(parts) == 3 && parts[0] == "brains" && (base == "tmp" || base == "cache") {
			return true
		}
		return base == "node_modules"
	}
	for _, suffix := range []string{".lock", "-wal", "-shm", "-journal", ".tmp"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

// visitingStays reports whether p lies in a visiting brain folder and
// stays out of the export by that folder's backup settings, and which
// folder it is.
func visitingStays(visiting []Folder, p string, d fs.DirEntry) (string, bool) {
	for _, f := range visiting {
		if !within(f.Dir, p) || filepath.Clean(p) == filepath.Clean(f.Dir) {
			continue
		}
		rel, err := filepath.Rel(f.Dir, p)
		if err != nil {
			return "", false
		}
		if (f.Excluded != nil && f.Excluded(filepath.ToSlash(rel))) || (d.IsDir() && folderJunk(p, d.Name())) {
			return f.Dir, true
		}
		return "", false
	}
	return "", false
}

// isDatabase reports whether a file is copied as a SQLite database.
// The bridge's are *.db; a brain's Codex state (brains/<id>/codex) keeps
// *.sqlite, whose -wal the export leaves out and VACUUM INTO takes in.
func isDatabase(rel string) bool {
	return strings.HasSuffix(rel, ".db") || strings.HasSuffix(rel, ".sqlite")
}

// ExportKey is the key of an export made at t.
func ExportKey(t time.Time) string {
	return ExportsPrefix + t.UTC().Format("20060102T150405Z") + keySuffix
}

// Export writes the hot tier of dataDir, encrypted to recipients, to store
// under core/exports/<UTC time>.tar.gz.age, then points core/exports/latest
// at it.
func Export(ctx context.Context, dataDir string, recipients []age.Recipient, store Store, opts ExportOptions) (ExportResult, error) {
	if len(recipients) == 0 {
		return ExportResult{}, errors.New("export: no age recipient to encrypt to")
	}
	if opts.DB == nil {
		return ExportResult{}, errors.New("export: no database copier")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return ExportResult{}, err
	}
	if st, err := os.Stat(dataDir); err != nil || !st.IsDir() {
		return ExportResult{}, fmt.Errorf("export: the data dir %s is not a folder: %v", dataDir, err)
	}
	if opts.TempDir == "" {
		opts.TempDir = filepath.Join(dataDir, "core", "export-tmp")
	}
	if err := os.MkdirAll(opts.TempDir, 0o700); err != nil {
		return ExportResult{}, err
	}
	snapDir, err := os.MkdirTemp(opts.TempDir, "snap-")
	if err != nil {
		return ExportResult{}, err
	}
	defer os.RemoveAll(snapDir)

	now := opts.Now()
	m := Manifest{Format: 1, Version: opts.Version, CreatedAt: now.UTC(), Hostname: opts.Hostname, DataDir: dataDir, DeviceID: opts.DeviceID, Move: opts.Move}
	along := map[string]bool{}
	for _, f := range opts.Folders {
		if f.Skip == "" && !within(dataDir, f.Dir) {
			along[filepath.Clean(f.Dir)] = true
		}
	}
	for _, f := range opts.BrainFolders {
		if !within(dataDir, f) && !along[filepath.Clean(f)] {
			m.OutsideBrainFolders = append(m.OutsideBrainFolders, f)
			opts.Logf("warning: brain folder %s is outside the data dir and not in the export: move it yourself", f)
		}
	}
	for _, f := range opts.Folders {
		if f.Skip != "" && !within(dataDir, f.Dir) {
			opts.Logf("warning: brain folder %s stays: %s", f.Dir, f.Skip)
		}
	}

	var visiting []Folder
	for _, f := range opts.Folders {
		if f.Visiting && within(dataDir, f.Dir) {
			visiting = append(visiting, f)
		}
	}
	left := map[string]int{}

	// First the list and the database snapshots, so the manifest that
	// leads the archive is complete.
	var items []item
	err = filepath.WalkDir(dataDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dataDir {
			return nil
		}
		rel, err := filepath.Rel(dataDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if excluded(rel, d.IsDir()) && !(opts.IncludeMedia && rel == "media") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if f, ok := visitingStays(visiting, p, d); ok {
			left[f]++
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			items = append(items, item{rel: rel, src: p, dir: true, mode: info.Mode().Perm(), mtime: info.ModTime()})
		case info.Mode().IsRegular() && isDatabase(rel):
			snap := filepath.Join(snapDir, fmt.Sprintf("%d.db", len(m.Databases)))
			if err := opts.DB.VacuumInto(ctx, p, snap); err != nil {
				return fmt.Errorf("copying the database %s: %w", rel, err)
			}
			m.Databases = append(m.Databases, rel)
			items = append(items, item{rel: rel, src: snap, mode: 0o600, mtime: info.ModTime()})
		case info.Mode().IsRegular():
			items = append(items, item{rel: rel, src: p, mode: info.Mode().Perm(), mtime: info.ModTime()})
		default:
			m.Skipped = append(m.Skipped, rel)
			opts.Logf("warning: %s is not a regular file or folder (%s): left out of the export", rel, info.Mode().Type())
		}
		return nil
	})
	if err != nil {
		return ExportResult{}, fmt.Errorf("export: %w", err)
	}
	for _, f := range visiting {
		if n := left[f.Dir]; n > 0 {
			opts.Logf("Leaving out %d files and folders of brain folder %s (%s), as its backup settings do on its way here", n, f.Dir, f.ID)
		}
	}
	for _, it := range items {
		if !it.dir {
			st, err := os.Stat(it.src)
			if err != nil {
				return ExportResult{}, err
			}
			m.Files++
			m.Bytes += st.Size()
		}
	}
	var folderMembers []item
	for _, f := range opts.Folders {
		if f.Skip != "" || within(dataDir, f.Dir) {
			continue
		}
		if !validFolderID.MatchString(f.ID) {
			return ExportResult{}, fmt.Errorf("export: brain id %q cannot name a folder in the archive", f.ID)
		}
		its, info, err := folderItems(f, opts.Logf)
		if err != nil {
			return ExportResult{}, fmt.Errorf("export: the brain folder %s: %w", f.Dir, err)
		}
		folderMembers = append(folderMembers, item{rel: foldersPrefix + f.ID, src: f.Dir, dir: true, mode: 0o700, mtime: now})
		folderMembers = append(folderMembers, its...)
		m.Folders = append(m.Folders, info)
		opts.Logf("Taking along brain folder %s (%s): %d files, %d bytes; %d left out by its backup settings and as programs of this machine", f.Dir, f.ID, info.Files, info.Bytes, info.Left)
	}
	if len(m.Folders) > 0 {
		m.Format = formatFolders
	}

	key := ExportKey(now)
	pr, pw := io.Pipe()
	counted := &countingWriter{w: pw}
	go func() {
		pw.CloseWithError(func() error {
			enc, err := age.Encrypt(counted, recipients...)
			if err != nil {
				return err
			}
			var inner io.WriteCloser = nopCloser{enc}
			if opts.SealKey != nil {
				if inner, err = seal.Encrypt(enc, opts.SealKey); err != nil {
					return err
				}
			}
			gz := gzip.NewWriter(inner)
			tw := tar.NewWriter(gz)
			mdata, err := json.MarshalIndent(m, "", "  ")
			if err != nil {
				return err
			}
			if err := tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(mdata)), ModTime: m.CreatedAt, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
				return err
			}
			if _, err := tw.Write(mdata); err != nil {
				return err
			}
			for _, it := range items {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := writeItem(tw, dataPrefix+it.rel, it.src, it.dir, it.mode, it.mtime); err != nil {
					return err
				}
			}
			for _, it := range folderMembers {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := writeItem(tw, it.rel, it.src, it.dir, it.mode, it.mtime); err != nil {
					return err
				}
			}
			return errors.Join(tw.Close(), gz.Close(), inner.Close(), enc.Close())
		}())
	}()
	if err := store.Put(ctx, key, pr); err != nil {
		pr.CloseWithError(err)
		return ExportResult{}, fmt.Errorf("export: uploading %s: %w", key, err)
	}
	info := Info{Key: key, Device: opts.DeviceID, Move: opts.Move, Version: opts.Version, Hostname: opts.Hostname, CreatedAt: m.CreatedAt, Bytes: counted.n}
	idata, err := json.Marshal(info)
	if err != nil {
		return ExportResult{}, err
	}
	if err := store.Put(ctx, InfoKey(key), bytes.NewReader(append(idata, '\n'))); err != nil {
		return ExportResult{}, fmt.Errorf("export: %s is uploaded, but its info failed: %w", key, err)
	}
	if err := store.Put(ctx, LatestKey, strings.NewReader(key+"\n")); err != nil {
		return ExportResult{}, fmt.Errorf("export: %s is uploaded, but pointing %s at it failed: %w", key, LatestKey, err)
	}
	opts.Logf("Exported %d files (%d bytes, %d databases) to %s", m.Files, m.Bytes, len(m.Databases), key)
	return ExportResult{Key: key, Manifest: m, Info: info}, nil
}

// countingWriter counts what goes through it.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func writeItem(tw *tar.Writer, name, src string, dir bool, mode fs.FileMode, mtime time.Time) error {
	if dir {
		return tw.WriteHeader(&tar.Header{Name: name + "/", Mode: int64(mode), ModTime: mtime, Typeflag: tar.TypeDir, Format: tar.FormatPAX})
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(mode), Size: st.Size(), ModTime: mtime, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
		return err
	}
	// A file that grew since the Stat would break the tar: copy what was
	// measured, and refuse one that shrank.
	n, err := io.Copy(tw, io.LimitReader(f, st.Size()))
	if err == nil && n != st.Size() {
		err = fmt.Errorf("%s shrank while it was exported", src)
	}
	return err
}

// within reports whether p is dir or below it; both absolute.
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// cleanMember checks a tar member's name and returns its path below the
// data dir: below data/, relative, with no "..", "." or empty part.
func cleanMember(name string) (string, error) {
	rel, ok := strings.CutPrefix(name, dataPrefix)
	rel = strings.TrimSuffix(rel, "/")
	if !ok || rel == "" || strings.Contains(rel, "\\") || path.Clean(rel) != rel || path.IsAbs(rel) {
		return "", fmt.Errorf("unsafe member %q in the archive", name)
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." || part == "." || part == "" {
			return "", fmt.Errorf("unsafe member %q in the archive", name)
		}
	}
	return rel, nil
}
