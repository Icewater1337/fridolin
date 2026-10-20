// Package coreexport moves the core's hot tier between machines: Export
// writes the data dir as one age-encrypted archive to the vault bucket,
// Import fetches, decrypts and checks one into a staging folder, and
// Activate puts a staged copy in place of a data dir that no bridge runs on
// (docs/CORE.md).
//
// The archive is a gzip-compressed tar: manifest.json first, then the data
// dir's files under data/. SQLite databases are copied with VACUUM INTO,
// never as files, so a live bridge's bridge.db is exported consistently.
// What any machine rebuilds or the cold tier holds completely is left out:
// runs/, run-tokens/, media/, vault/, core/ (the lease's device id and the
// export's own staging), lock files, SQLite's sidecars, the brains' tmp/ and
// cache/ folders, node_modules/ and *.tmp files.
package coreexport

import (
	"archive/tar"
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
)

// Store is what export and import need of the object store: the subset of
// cloud.ObjectStore they use. Get of a missing key returns an error that
// matches fs.ErrNotExist.
type Store interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key string, r io.Reader) error
}

// Keys in the vault bucket.
const (
	ExportsPrefix = "core/exports/"
	LatestKey     = ExportsPrefix + "latest"
	keySuffix     = ".tar.gz.age"
	manifestName  = "manifest.json"
	dataPrefix    = "data/"
	// FormatVersion is the archive's format; Import refuses a newer one.
	FormatVersion = 1
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
	// the manifest and warned about.
	BrainFolders []string
	// TempDir holds the database snapshots while the archive is written;
	// default <dataDir>/core/export-tmp, on the same disk as the data.
	TempDir string
	Now     func() time.Time
	Logf    func(format string, args ...any)
}

// ExportResult is what Export wrote.
type ExportResult struct {
	Key      string
	Manifest Manifest
}

// excluded reports whether rel, a slash path below the data dir, stays out
// of an export, and whether it is a folder.
func excluded(rel string, dir bool) bool {
	parts := strings.Split(rel, "/")
	base := parts[len(parts)-1]
	if dir {
		if len(parts) == 1 && slices.Contains([]string{"runs", "run-tokens", "media", "vault", "core"}, base) {
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

// isDatabase reports whether a file is copied as a SQLite database.
func isDatabase(rel string) bool { return strings.HasSuffix(rel, ".db") }

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
	m := Manifest{Format: FormatVersion, Version: opts.Version, CreatedAt: now.UTC(), Hostname: opts.Hostname, DataDir: dataDir}
	for _, f := range opts.BrainFolders {
		if !within(dataDir, f) {
			m.OutsideBrainFolders = append(m.OutsideBrainFolders, f)
			opts.Logf("warning: brain folder %s is outside the data dir and not in the export: move it yourself", f)
		}
	}

	// First the list and the database snapshots, so the manifest that
	// leads the archive is complete.
	type item struct {
		rel, src string
		dir      bool
		mode     fs.FileMode
		mtime    time.Time
	}
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
		if excluded(rel, d.IsDir()) {
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

	key := ExportKey(now)
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(func() error {
			enc, err := age.Encrypt(pw, recipients...)
			if err != nil {
				return err
			}
			gz := gzip.NewWriter(enc)
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
			return errors.Join(tw.Close(), gz.Close(), enc.Close())
		}())
	}()
	if err := store.Put(ctx, key, pr); err != nil {
		pr.CloseWithError(err)
		return ExportResult{}, fmt.Errorf("export: uploading %s: %w", key, err)
	}
	if err := store.Put(ctx, LatestKey, strings.NewReader(key+"\n")); err != nil {
		return ExportResult{}, fmt.Errorf("export: %s is uploaded, but pointing %s at it failed: %w", key, LatestKey, err)
	}
	opts.Logf("Exported %d files (%d bytes, %d databases) to %s", m.Files, m.Bytes, len(m.Databases), key)
	return ExportResult{Key: key, Manifest: m}, nil
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
