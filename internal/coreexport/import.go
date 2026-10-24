package coreexport

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/Icewater1337/fridolin/internal/seal"
)

// ImportOptions are Import's settings besides the store, identities and
// staging folder.
type ImportOptions struct {
	// Key is the export to fetch; "" fetches the one core/exports/latest
	// names.
	Key string
	// SealKey opens the inner layer of an export that has one. An export
	// made before it (age alone) imports without it.
	SealKey []byte
	DB      DB
	Logf    func(format string, args ...any)
}

// ImportResult is what Import staged.
type ImportResult struct {
	Key      string
	Manifest Manifest
	// Staged is the folder that holds the data dir's files.
	Staged string
}

// maxManifest bounds the manifest a decrypted archive may carry.
const maxManifest = 16 << 20

// Latest returns the key core/exports/latest points at.
func Latest(ctx context.Context, store Store) (string, error) {
	rc, err := store.Get(ctx, LatestKey)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("there is no export yet (%s is missing): run fridolin core export on the core first", LatestKey)
		}
		return "", err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 4096))
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(data))
	if !strings.HasPrefix(key, ExportsPrefix) || !strings.HasSuffix(key, keySuffix) || strings.Contains(key, "..") {
		return "", fmt.Errorf("%s points at %q, which is no export", LatestKey, key)
	}
	return key, nil
}

// Import fetches an export, decrypts it with identities, checks every
// member and writes it into staging, which must be a new or empty folder,
// then runs SQLite's integrity check on every database in it. Nothing
// outside staging is touched: Activate puts it in place.
func Import(ctx context.Context, store Store, identities []age.Identity, staging string, opts ImportOptions) (ImportResult, error) {
	if len(identities) == 0 {
		return ImportResult{}, errors.New("import: no age identity to decrypt with")
	}
	if opts.DB == nil {
		return ImportResult{}, errors.New("import: no database checker")
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	key := opts.Key
	if key == "" {
		var err error
		if key, err = Latest(ctx, store); err != nil {
			return ImportResult{}, err
		}
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return ImportResult{}, err
	}
	if entries, err := os.ReadDir(staging); err != nil {
		return ImportResult{}, err
	} else if len(entries) > 0 {
		return ImportResult{}, fmt.Errorf("import: the staging folder %s is not empty", staging)
	}
	rc, err := store.Get(ctx, key)
	if err != nil {
		return ImportResult{}, fmt.Errorf("import: fetching %s: %w", key, err)
	}
	defer rc.Close()
	dec, err := age.Decrypt(rc, identities...)
	if err != nil {
		return ImportResult{}, fmt.Errorf("import: decrypting %s: %w (is FRIDOLIN_AGE_IDENTITY the identity the export was made for?)", key, err)
	}
	plain, sealed, err := openInner(dec, opts.SealKey)
	if err != nil {
		return ImportResult{}, fmt.Errorf("import: %s: %w", key, err)
	}
	if !sealed {
		opts.Logf("note: %s has one layer of encryption (age), from before exports were sealed as well", key)
	}
	gz, err := gzip.NewReader(plain)
	if err != nil {
		return ImportResult{}, fmt.Errorf("import: %s: %w", key, err)
	}
	m, err := extract(ctx, tar.NewReader(gz), staging)
	if err != nil {
		return ImportResult{}, fmt.Errorf("import: %s: %w", key, err)
	}
	for _, db := range m.Databases {
		if err := opts.DB.IntegrityCheck(ctx, filepath.Join(staging, filepath.FromSlash(db))); err != nil {
			return ImportResult{}, fmt.Errorf("import: %s: %w", key, err)
		}
	}
	for _, f := range m.OutsideBrainFolders {
		opts.Logf("warning: brain folder %s was outside the exported data dir and is not in the export: bring it over yourself", f)
	}
	opts.Logf("Staged %d files (%d bytes) of %s from %s, made %s by %s", m.Files, m.Bytes, key, m.Hostname, m.CreatedAt.Format(time.RFC3339), m.Version)
	return ImportResult{Key: key, Manifest: m, Staged: staging}, nil
}

// extract writes the archive's data members below staging and returns its
// manifest, checking that the members match it.
func extract(ctx context.Context, tr *tar.Reader, staging string) (Manifest, error) {
	var m Manifest
	hdr, err := tr.Next()
	if err != nil {
		return m, fmt.Errorf("reading the archive: %w", err)
	}
	if hdr.Name != manifestName || hdr.Size > maxManifest {
		return m, fmt.Errorf("the archive does not start with its manifest")
	}
	mdata, err := io.ReadAll(io.LimitReader(tr, maxManifest))
	if err != nil {
		return m, err
	}
	if err := json.NewDecoder(bytes.NewReader(mdata)).Decode(&m); err != nil {
		return m, fmt.Errorf("the archive's manifest: %w", err)
	}
	if m.Format < 1 || m.Format > FormatVersion {
		return m, fmt.Errorf("the archive has format %d; this version reads up to %d: update fridolin", m.Format, FormatVersion)
	}
	seen := map[string]bool{}
	files := 0
	var bytesTotal int64
	type count struct {
		files int
		bytes int64
	}
	folders := map[string]*count{}
	for _, f := range m.Folders {
		if !validFolderID.MatchString(f.ID) || folders[f.ID] != nil {
			return m, fmt.Errorf("the manifest names brain folder %q twice or badly", f.ID)
		}
		folders[f.ID] = &count{}
	}
	for {
		if err := ctx.Err(); err != nil {
			return m, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return m, fmt.Errorf("reading the archive: %w", err)
		}
		var rel, dst string
		var folder *count
		if strings.HasPrefix(hdr.Name, foldersPrefix) {
			id, below, err := cleanFolderMember(hdr.Name)
			if err != nil {
				return m, err
			}
			if folder = folders[id]; folder == nil {
				return m, fmt.Errorf("the archive holds brain folder %s, which its manifest does not list", id)
			}
			rel = foldersPrefix + id + "/" + below
			dst = filepath.Join(stagedFolders(staging), id, filepath.FromSlash(below))
		} else {
			if rel, err = cleanMember(hdr.Name); err != nil {
				return m, err
			}
			dst = filepath.Join(staging, filepath.FromSlash(rel))
		}
		if seen[rel] {
			return m, fmt.Errorf("%s is in the archive twice", rel)
		}
		seen[rel] = true
		// The cleaners already refuse ".." parts, absolute names and
		// backslashes; the containment check says so where the write happens.
		if !strings.HasPrefix(dst, filepath.Clean(staging)+string(filepath.Separator)) {
			return m, fmt.Errorf("unsafe member %q in the archive", hdr.Name)
		}
		// Owner bits only: the data dir is this user's alone.
		mode := fs.FileMode(hdr.Mode).Perm() & 0o700
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, mode|0o700); err != nil {
				return m, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return m, err
			}
			n, err := writeFile(dst, tr, mode|0o600)
			if err != nil {
				return m, err
			}
			if n != hdr.Size {
				return m, fmt.Errorf("%s is cut short", rel)
			}
			if folder != nil {
				folder.files++
				folder.bytes += n
				continue
			}
			files++
			bytesTotal += n
		default:
			return m, fmt.Errorf("%s is not a regular file or folder (type %q)", rel, hdr.Typeflag)
		}
	}
	if files != m.Files || bytesTotal != m.Bytes {
		return m, fmt.Errorf("the archive holds %d files (%d bytes), its manifest says %d (%d bytes)", files, bytesTotal, m.Files, m.Bytes)
	}
	for _, db := range m.Databases {
		if !seen[db] {
			return m, fmt.Errorf("the database %s the manifest lists is not in the archive", db)
		}
	}
	for _, f := range m.Folders {
		if c := folders[f.ID]; c.files != f.Files || c.bytes != f.Bytes || !seen[foldersPrefix+f.ID+"/"] {
			return m, fmt.Errorf("the archive holds %d files (%d bytes) of brain folder %s, its manifest says %d (%d bytes)", c.files, c.bytes, f.ID, f.Files, f.Bytes)
		}
	}
	return m, nil
}

func writeFile(dst string, r io.Reader, mode fs.FileMode) (int64, error) {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// keep are the entries of a data dir that activation neither counts as
// data nor moves: the lease's state and import staging, and the lock.
var keep = []string{"core", "bridge.lock"}

// ErrTargetInUse is Activate's refusal of a data dir with data in it,
// without force.
var ErrTargetInUse = errors.New("the data dir is not empty")

// Activate moves the staged data dir's files into target, which no bridge
// may be running on: lock takes target's instance lock (platform.AcquireLock
// in production) and holds it throughout. A target with data in it is
// refused unless force is set; with force its entries move to
// core/replaced-<time>/ in it first, never deleted. Each entry moves with
// one rename, so staged must be on target's disk (Import's default staging
// folder is in target's core/).
func Activate(staged, target string, force bool, now time.Time, lock func(string) (func(), error)) error {
	return ActivateWith(staged, target, ActivateOptions{Force: force, Now: now, Lock: lock})
}

// ActivateOptions are ActivateWith's settings.
type ActivateOptions struct {
	Force bool
	Now   time.Time
	Lock  func(string) (func(), error)
	// Aside is where target's entries go with Force; default
	// core/replaced-<time>/ in target. It must be on target's disk.
	Aside string
	// Keep are more entries of target that stay where they are and are
	// not counted as data, besides core/ and bridge.lock: a native data
	// dir's migration/ folder, say, which holds Aside.
	Keep []string
}

// ActivateWith is Activate with the aside folder and the entries that stay
// of opts.
func ActivateWith(staged, target string, opts ActivateOptions) error {
	unlock, err := opts.Lock(target)
	if err != nil {
		return fmt.Errorf("activate: %w (stop the core first: a running bridge holds the data dir)", err)
	}
	defer unlock()
	stay := append(slices.Clone(keep), opts.Keep...)
	existing, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	var data []string
	for _, e := range existing {
		if !slices.Contains(stay, e.Name()) {
			data = append(data, e.Name())
		}
	}
	if len(data) > 0 {
		aside := opts.Aside
		if aside == "" {
			aside = filepath.Join(target, "core", "replaced-"+opts.Now.UTC().Format("20060102T150405Z"))
		}
		if !opts.Force {
			return fmt.Errorf("activate: %w: %s holds %s; pass --force to move them to %s first", ErrTargetInUse, target, strings.Join(data, ", "), aside)
		}
		if err := os.MkdirAll(aside, 0o700); err != nil {
			return err
		}
		for _, name := range data {
			if err := os.Rename(filepath.Join(target, name), filepath.Join(aside, name)); err != nil {
				return fmt.Errorf("activate: moving %s aside: %w", name, err)
			}
		}
	}
	entries, err := os.ReadDir(staged)
	if err != nil {
		return err
	}
	// The buckets' encryption state comes with the export; the one here
	// stays beside it as encryption.json.before-<time>.
	if src := filepath.Join(staged, filepath.FromSlash(EncryptionState)); exists(src) {
		dst := filepath.Join(target, filepath.FromSlash(EncryptionState))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if exists(dst) {
			if err := os.Rename(dst, dst+".before-"+opts.Now.UTC().Format("20060102T150405Z")); err != nil {
				return fmt.Errorf("activate: keeping the encryption state: %w", err)
			}
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("activate: moving the encryption state into place: %w", err)
		}
		_ = os.Remove(filepath.Join(staged, "core"))
	}
	for _, e := range entries {
		if slices.Contains(stay, e.Name()) {
			continue
		}
		if err := os.Rename(filepath.Join(staged, e.Name()), filepath.Join(target, e.Name())); err != nil {
			return fmt.Errorf("activate: moving %s into place: %w", e.Name(), err)
		}
	}
	// Empty now; a leftover is harmless, so only an empty folder goes.
	_ = os.Remove(staged)
	return nil
}

// FixDataDir points the data_dir of the imported config.json in dataDir at
// dataDir itself, when it names another folder: an export from a native
// install (~/.fridolin) imported into the container (/data) would
// otherwise start on a folder that is not the volume. Other keys keep
// their values; db_path, media_dir and brain_dirs that point outside
// dataDir are returned as warnings, since only the person knows where
// those went; a brain folder that is there (bound into the container at
// its own path) is not, with home the folder a leading ~/ means.
func FixDataDir(dataDir, home string) (warnings []string, err error) {
	path := filepath.Join(dataDir, "config.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var old string
	_ = json.Unmarshal(cfg["data_dir"], &old)
	for _, k := range []string{"db_path", "media_dir"} {
		var v string
		if json.Unmarshal(cfg[k], &v) == nil && v != "" && !within(dataDir, v) {
			warnings = append(warnings, fmt.Sprintf("%s is %s, outside the data dir: check it before the first start", k, v))
		}
	}
	// Each entry is a path, or an object with one ({"path": ..., "approved": ...}).
	var entries []json.RawMessage
	var dirs []string
	_ = json.Unmarshal(cfg["brain_dirs"], &entries)
	for _, e := range entries {
		var ref struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(e, &ref.Path) != nil {
			_ = json.Unmarshal(e, &ref)
		}
		if ref.Path != "" {
			dirs = append(dirs, ref.Path)
		}
	}
	for _, d := range dirs {
		// A folder that is there (bound into the container at its own path
		// from the machine it came from) needs nothing.
		p := d
		if rest, ok := strings.CutPrefix(d, "~/"); ok && home != "" {
			p = filepath.Join(home, rest)
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			continue
		}
		if !within(dataDir, d) {
			warnings = append(warnings, fmt.Sprintf("brain folder %s is outside the data dir: bring it over, or change brain_dirs", d))
		}
	}
	if old == dataDir {
		return warnings, nil
	}
	raw, _ := json.Marshal(dataDir)
	cfg["data_dir"] = raw
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	tmp := path + ".import.tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return append(warnings, fmt.Sprintf("data_dir in config.json was %q; it is now %q", old, dataDir)), nil
}

// openInner opens the seal layer under age when the archive has one
// (the header FRDSEAL1), and passes an older archive's gzip stream on.
func openInner(r io.Reader, key []byte) (io.Reader, bool, error) {
	br := bufio.NewReader(r)
	head, err := br.Peek(len(sealMagic))
	if err != nil {
		return nil, false, fmt.Errorf("reading the archive: %w", err)
	}
	if string(head) != sealMagic {
		return br, false, nil
	}
	if key == nil {
		return nil, true, fmt.Errorf("the export is sealed and needs the export key (%s)", "FRIDOLIN_EXPORT_KEY")
	}
	pr, err := seal.Decrypt(br, key)
	if err != nil {
		return nil, true, err
	}
	return pr, true, nil
}

// sealMagic starts every sealed stream.
const sealMagic = "FRDSEAL1"

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
