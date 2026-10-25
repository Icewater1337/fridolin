package app

import (
	"archive/tar"
	"cmp"
	"context"
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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/snapshot"
)

// The secret folders (docs/VAULT.md, "Secret folders"):
// sandbox.credentials_dir (the Google OAuth client, the calendar
// credentials and tokens, the dashboard users) and vault.secret_dirs. The
// vault backs them up to B2 only, through the cold tier's two layers
// (rclone crypt outside, seal inside) into a folder of their own in the
// vault bucket, never to Google Drive or a node target, and keeps them out
// of the snapshots and the cold tier, which go there or evict. It only
// reads them: every file is opened read-only, nothing in them is written,
// moved or deleted; only a restore with --force writes there, and only
// after copying what it replaces aside.
//
// A version is one tar of every folder, streamed through both layers
// (nothing of it is written to disk here), as <UTC time>.tar: a manifest
// first, then each folder under its label with its files' bytes, modes and
// times. One goes up each day, and after a change once the folders were
// quiet for a tick, at most every secretFoldersChange. The log and the
// status name folders and count files and bytes, never a file's content.

// secretFoldersPath is their folder in the vault bucket, behind rclone
// crypt with the cold tier's passwords and sealed with its key.
const secretFoldersPath = "secret-folders-sealed"

// secretManifestName is the archive's first entry.
const secretManifestName = "fridolin-secret-folders.json"

// The bounds of what the vault takes for secret folders: they hold
// credentials, not documents.
const (
	secretFoldersMaxFiles = 10000
	secretFoldersMaxBytes = 64 << 20
)

// The rhythm: a version each day whether or not anything changed, a
// failed one tried again after secretFoldersRetry, and every version of
// the last secretFoldersRecent kept besides one a day.
const (
	secretFoldersDaily  = 24 * time.Hour
	secretFoldersRetry  = 10 * time.Minute
	secretFoldersRecent = 24 * time.Hour
)

// secretVersionLayout names a version: its UTC time.
const secretVersionLayout = "20060102T150405Z"

// secretFolder is one folder the vault backs up.
type secretFolder struct {
	// Path is the folder, absolute; Source the setting that names it.
	Path   string `json:"path"`
	Source string `json:"source"`
	// Label is its folder in a version and in a restore.
	Label string `json:"label"`
}

// secretFoldersOf lists the folders cfg names: sandbox.credentials_dir
// first, then vault.secret_dirs, each once. One inside another is an
// error: the outer one would carry it twice; so is the data dir or a
// folder holding it, which the vault keeps otherwise.
func secretFoldersOf(cfg *config.Settings) ([]secretFolder, error) {
	var out []secretFolder
	labels := map[string]bool{}
	data := filepath.Clean(AbsPath(cfg.DataDir))
	add := func(p, source string) error {
		if p == "" {
			return nil
		}
		p = filepath.Clean(AbsPath(p))
		if fsx.Within(data, p) {
			return fmt.Errorf("%s %s is or holds the data dir, which the cold tier and the snapshots keep: name a folder of its own", source, p)
		}
		for _, f := range out {
			switch {
			case f.Path == p:
				return nil
			case fsx.Within(p, f.Path) || fsx.Within(f.Path, p):
				return fmt.Errorf("%s %s and %s %s lie one inside the other: name the outer one only", f.Source, f.Path, source, p)
			}
		}
		label := filepath.Base(p)
		for n := 2; labels[label]; n++ {
			label = filepath.Base(p) + "-" + strconv.Itoa(n)
		}
		labels[label] = true
		out = append(out, secretFolder{Path: p, Source: source, Label: label})
		return nil
	}
	if err := add(cfg.Sandbox.CredentialsDir, "sandbox.credentials_dir"); err != nil {
		return out, err
	}
	for _, d := range cfg.Vault.SecretDirs {
		if err := add(d, "vault.secret_dirs"); err != nil {
			return out, err
		}
	}
	return out, nil
}

// secretFolderPaths are the folders cfg names, absolute, for what must
// leave them out (the snapshots, the cold tier); an overlap still names
// both. The data dir, or a folder holding it, is not one: leaving that out
// would leave out everything (a credentials_dir set to the data dir, where
// the calendar tokens are when it is not set).
func secretFolderPaths(cfg *config.Settings) []string {
	var out []string
	data := filepath.Clean(AbsPath(cfg.DataDir))
	for _, p := range append([]string{cfg.Sandbox.CredentialsDir}, cfg.Vault.SecretDirs...) {
		if p == "" {
			continue
		}
		if p = filepath.Clean(AbsPath(p)); !fsx.Within(data, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// secretEntry is one folder or file of a secret folder, as scanned.
type secretEntry struct {
	folder int    // index in the folders
	rel    string // slash-separated below the folder; "" for the folder itself
	abs    string
	mode   fs.FileMode
	size   int64
	mtime  time.Time
}

// secretFolderView is one folder in the status: its numbers, never its
// files.
type secretFolderView struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Label  string `json:"label"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
	// Skipped counts what is neither a folder nor a regular file
	// (symbolic links and the like), which a version leaves out.
	Skipped int    `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
}

// scanSecretFolders walks the folders without opening a file: what a
// version would hold, each folder's numbers, and a fingerprint of names,
// sizes, modes and times that changes with any of them.
func scanSecretFolders(folders []secretFolder) ([]secretEntry, []secretFolderView, string, error) {
	var entries []secretEntry
	views := make([]secretFolderView, len(folders))
	var errs []error
	files, bytes := 0, int64(0)
	for i, f := range folders {
		v := &views[i]
		v.Path, v.Source, v.Label = f.Path, f.Source, f.Label
		root, err := filepath.EvalSymlinks(f.Path)
		if err == nil {
			var fi os.FileInfo
			if fi, err = os.Stat(root); err == nil && !fi.IsDir() {
				err = errors.New("is not a folder")
			}
		}
		if err != nil {
			v.Error = errorWithoutPath(err, f.Path)
			errs = append(errs, fmt.Errorf("%s %s: %s", f.Source, f.Path, v.Error))
			continue
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if rel == "." {
				rel = ""
			}
			switch {
			case fi.IsDir():
			case fi.Mode().IsRegular():
				v.Files++
				v.Bytes += fi.Size()
				files++
				bytes += fi.Size()
				if files > secretFoldersMaxFiles || bytes > secretFoldersMaxBytes {
					return fmt.Errorf("the secret folders hold more than %d files or %s: they are for credentials, not documents", secretFoldersMaxFiles, gib(secretFoldersMaxBytes))
				}
			default:
				v.Skipped++
				return nil
			}
			entries = append(entries, secretEntry{folder: i, rel: rel, abs: p, mode: fi.Mode(), size: fi.Size(), mtime: fi.ModTime()})
			return nil
		})
		if err != nil {
			v.Error = errorWithoutPath(err, root)
			errs = append(errs, fmt.Errorf("%s %s: %s", f.Source, f.Path, v.Error))
		}
	}
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%s\x00%o\x00%d\x00%d\n", folders[e.folder].Label, e.rel, e.mode, e.size, e.mtime.UnixNano())
	}
	return entries, views, hex.EncodeToString(h.Sum(nil)), errors.Join(errs...)
}

// errorWithoutPath is err's text with p written as "it", so a view does
// not say the folder's path twice.
func errorWithoutPath(err error, p string) string {
	var pe *fs.PathError
	if errors.As(err, &pe) && pe.Path == p {
		return pe.Op + ": " + pe.Err.Error()
	}
	return err.Error()
}

// secretManifest is a version's first entry: which folder is which label.
type secretManifest struct {
	Version int                `json:"version"`
	At      time.Time          `json:"at"`
	Folders []secretFolderView `json:"folders"`
}

// writeSecretArchive writes the version of entries to w: the manifest,
// then each folder and file. Files are opened read-only; one that changed
// size since the scan fails the version, which the next tick takes again.
func writeSecretArchive(w io.Writer, man secretManifest, folders []secretFolder, entries []secretEntry) error {
	tw := tar.NewWriter(w)
	data, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: secretManifestName, Mode: 0o600, Size: int64(len(data)), ModTime: man.At, Format: tar.FormatPAX}); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	for _, e := range entries {
		name := path.Join(folders[e.folder].Label, e.rel)
		if e.mode.IsDir() {
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: int64(e.mode.Perm()), ModTime: e.mtime, Format: tar.FormatPAX}); err != nil {
				return err
			}
			continue
		}
		if err := writeSecretFile(tw, name, e); err != nil {
			return err
		}
	}
	return tw.Close()
}

func writeSecretFile(tw *tar.Writer, name string, e secretEntry) error {
	f, err := os.Open(e.abs) // read-only: the vault never writes here
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is no longer a regular file", e.abs)
	}
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: int64(fi.Mode().Perm()), Size: fi.Size(), ModTime: fi.ModTime(), Format: tar.FormatPAX}); err != nil {
		return err
	}
	if n, err := io.CopyN(tw, f, fi.Size()); err != nil {
		return fmt.Errorf("%s changed while it was read (%d of %d bytes): %w", e.abs, n, fi.Size(), err)
	}
	return nil
}

// secretBackupRecord is one version taken, or tried.
type secretBackupRecord struct {
	At      time.Time `json:"at,omitzero"`
	Version string    `json:"version,omitempty"`
	// Reason is why it was taken: first, daily, changed or asked.
	Reason string `json:"reason"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
	Error  string `json:"error,omitempty"`
}

// secretVersion is one version in the bucket.
type secretVersion struct {
	Version string    `json:"version"`
	At      time.Time `json:"at"`
	Bytes   int64     `json:"bytes"`
}

// secretRestoreRecord is the last restore.
type secretRestoreRecord struct {
	At      time.Time `json:"at,omitzero"`
	Version string    `json:"version,omitempty"`
	// To is where it was restored: a new folder, or "the live folders"
	// with Force, whose replaced files went to Replaced first.
	To       string `json:"to,omitempty"`
	Force    bool   `json:"force,omitempty"`
	Replaced string `json:"replaced,omitempty"`
	// Folders are the folders of the version, by label, with the path
	// they were backed up from.
	Folders []secretFolderView `json:"folders,omitempty"`
	Files   int                `json:"files"`
	Bytes   int64              `json:"bytes"`
	// Kept counts the files of the live folders a forced restore left as
	// they were, as the version does not have them.
	Kept  int    `json:"kept,omitempty"`
	Error string `json:"error,omitempty"`
}

// secretFoldersStatus is vaultStatus.SecretFolders; it is omitted while
// no secret folder is set.
type secretFoldersStatus struct {
	// Enabled says the store is there; Error says why not.
	Enabled  bool               `json:"enabled"`
	Error    string             `json:"error,omitempty"`
	Where    string             `json:"where,omitempty"`
	KeepDays int                `json:"keep_days"`
	Folders  []secretFolderView `json:"folders"`
	// Last is the last version tried, LastOK the last that went up.
	Last   *secretBackupRecord `json:"last,omitempty"`
	LastOK *secretBackupRecord `json:"last_ok,omitempty"`
	// Versions are the versions in the bucket, the oldest first, as the
	// last backup or prune listed them.
	Versions []secretVersion `json:"versions,omitempty"`
	// Fingerprint is the folders' as the last version took them: names,
	// sizes, modes and times, hashed.
	Fingerprint string `json:"fingerprint,omitempty"`
	// PruneHold is a version that lost most of the files of the one
	// before: pruning waits until the person allows it
	// (POST /api/vault/prune/allow), as for the snapshots.
	PruneHold   *deletionHold        `json:"prune_hold,omitempty"`
	LastPrune   *vaultRecord         `json:"last_prune,omitempty"`
	LastRestore *secretRestoreRecord `json:"last_restore,omitempty"`
}

// vaultSecretFolders backs up the secret folders of one vault run.
type vaultSecretFolders struct {
	s     *vaultService
	store cloud.ObjectStore // nil when there is none: Error says why
	err   string
	where string
	// mu keeps backups and restores one at a time: the tick skips a round
	// while one runs, a backup asked for and a restore wait.
	mu sync.Mutex
	// seen is the fingerprint the last tick saw: a change is backed up
	// once the next tick sees the same, so a sign-in that writes a few
	// files goes up once.
	seenMu sync.Mutex
	seen   string
}

func newVaultSecretFolders(s *vaultService, b vaultBackends) *vaultSecretFolders {
	return &vaultSecretFolders{s: s, store: b.secretFolders, err: b.secretFoldersErr, where: "vault bucket, " + secretFoldersPath + "/"}
}

// folders are the folders the current settings name.
func (sf *vaultSecretFolders) folders() (*config.Settings, []secretFolder, error) {
	cfg := sf.s.cfgs.Get()
	folders, err := secretFoldersOf(cfg)
	return cfg, folders, err
}

func (sf *vaultSecretFolders) update(fn func(st *secretFoldersStatus)) {
	sf.s.mu.Lock()
	defer sf.s.mu.Unlock()
	st := sf.s.status.SecretFolders
	if st == nil {
		st = &secretFoldersStatus{}
		sf.s.status.SecretFolders = st
	}
	fn(st)
	sf.s.saveLocked()
}

func (sf *vaultSecretFolders) state() secretFoldersStatus {
	sf.s.mu.Lock()
	defer sf.s.mu.Unlock()
	if st := sf.s.status.SecretFolders; st != nil {
		return *st
	}
	return secretFoldersStatus{}
}

// tick backs up what is due, in the background, unless a backup or a
// restore runs.
func (sf *vaultSecretFolders) tick(ctx context.Context, now time.Time) {
	cfg, folders, err := sf.folders()
	if err == nil && len(folders) == 0 {
		sf.s.mu.Lock()
		if sf.s.status.SecretFolders != nil {
			sf.s.status.SecretFolders = nil
			sf.s.saveLocked()
		}
		sf.s.mu.Unlock()
		return
	}
	var fp string
	if err == nil {
		_, _, fp, err = scanSecretFolders(folders)
	}
	sf.seenMu.Lock()
	stable := fp != "" && fp == sf.seen
	sf.seen = fp
	sf.seenMu.Unlock()
	st := sf.state()
	reason := ""
	switch {
	case sf.store == nil:
		if st.Error != sf.err || st.Enabled || st.KeepDays != cfg.Vault.SecretKeepDays {
			sf.update(func(st *secretFoldersStatus) {
				st.Enabled, st.Error, st.Where, st.KeepDays = false, sf.err, "", cfg.Vault.SecretKeepDays
			})
		}
		return
	case st.Last != nil && st.Last.Error != "" && now.Sub(st.Last.At) < secretFoldersRetry:
		return
	case st.LastOK == nil:
		reason = "first"
	case now.Sub(st.LastOK.At) >= secretFoldersDaily:
		reason = "daily"
	case err != nil:
		if st.Last == nil || st.Last.Error == "" {
			reason = "changed" // a folder went missing: say so now
		}
	case fp != st.Fingerprint && stable && now.Sub(st.Last.At) >= cmp.Or(sf.s.secretChange, defaultWaits().secretFoldersChange):
		reason = "changed"
	}
	if reason == "" {
		return
	}
	if !sf.mu.TryLock() {
		return
	}
	go func() {
		defer sf.mu.Unlock()
		sf.backupLocked(ctx, reason)
	}()
}

// BackupSecretFolders takes a version now (POST
// /api/vault/secret-folders/backup, fridolin vault backup-secrets).
func (s *vaultService) BackupSecretFolders() error {
	return s.startJob("secret-folders backup", func(ctx context.Context, run *vaultRun) error {
		sf := run.secrets
		if sf == nil || sf.store == nil {
			return errors.New("the secret folders are not backed up: " + run.secretsWhyNot())
		}
		if _, folders, err := sf.folders(); err == nil && len(folders) == 0 {
			return errors.New("no secret folder is set: sandbox.credentials_dir or vault.secret_dirs")
		}
		sf.mu.Lock()
		defer sf.mu.Unlock()
		if rec := sf.backupLocked(ctx, "asked"); rec.Error != "" {
			return errors.New(rec.Error)
		}
		return nil
	})
}

func (run *vaultRun) secretsWhyNot() string {
	if run.secrets != nil && run.secrets.err != "" {
		return run.secrets.err
	}
	return "the vault has no store for them"
}

// backupLocked takes one version and prunes; the caller holds sf.mu.
func (sf *vaultSecretFolders) backupLocked(ctx context.Context, reason string) *secretBackupRecord {
	now := time.Now().UTC().Truncate(time.Second)
	rec := &secretBackupRecord{At: now, Reason: reason}
	cfg, folders, err := sf.folders()
	var entries []secretEntry
	var views []secretFolderView
	var fp string
	if err == nil {
		entries, views, fp, err = scanSecretFolders(folders)
	}
	if err == nil {
		for _, v := range views {
			rec.Files += v.Files
			rec.Bytes += v.Bytes
		}
		rec.Version = now.Format(secretVersionLayout) + ".tar"
		err = sf.put(ctx, rec.Version, secretManifest{Version: 1, At: now, Folders: views}, folders, entries)
	}
	if ctx.Err() != nil {
		return rec
	}
	if err != nil {
		rec.Version, rec.Error = "", err.Error()
		sf.s.log.Warn("vault: backing up the secret folders failed", "folders", len(folders), "err", err)
	} else {
		sf.s.log.Info("vault: backed up the secret folders", "version", rec.Version, "folders", len(folders), "files", rec.Files, "bytes", rec.Bytes, "reason", reason)
	}
	var prev *secretBackupRecord
	sf.update(func(st *secretFoldersStatus) {
		st.Enabled, st.Error, st.Where, st.KeepDays = true, "", sf.where, cfg.Vault.SecretKeepDays
		if views != nil {
			st.Folders = views
		}
		st.Last = rec
		if err == nil {
			prev, st.LastOK, st.Fingerprint = st.LastOK, rec, fp
			if prev != nil && snapshot.MassDeletion(prev.Files, rec.Files) && st.PruneHold == nil {
				st.PruneHold = &deletionHold{Since: rec.At, Snapshot: rec.Version, Kind: "secret-folders", PrevFiles: prev.Files, Files: rec.Files}
				sf.s.log.Warn("vault: a version of the secret folders lost most of the files of the one before; pruning waits until you allow it on the Vault page",
					"files", rec.Files, "before", prev.Files)
			}
		}
	})
	if err == nil {
		sf.pruneLocked(ctx, now, cfg.Vault.SecretKeepDays)
	}
	return rec
}

// put uploads one version, streamed, and checks that it is there; a
// version that did not go up whole is deleted again.
func (sf *vaultSecretFolders) put(ctx context.Context, key string, man secretManifest, folders []secretFolder, entries []secretEntry) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeSecretArchive(pw, man, folders, entries)) }()
	err := sf.store.Put(ctx, key, pr)
	pr.CloseWithError(errors.New("vault: the secret folders' upload ended"))
	if err == nil {
		var info cloud.ObjectInfo
		if info, err = sf.store.Stat(ctx, key); err == nil && info.Size <= 0 {
			err = errors.New("the version uploaded is empty")
		}
	}
	if err != nil {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		_ = sf.store.Delete(dctx, key)
		cancel()
	}
	return err
}

// listVersions lists the versions in the bucket, the oldest first.
func (sf *vaultSecretFolders) listVersions(ctx context.Context) ([]secretVersion, error) {
	objs, err := sf.store.List(ctx, "")
	if err != nil {
		return nil, err
	}
	var out []secretVersion
	for _, o := range objs {
		if v, ok := parseSecretVersion(o.Key); ok {
			v.Bytes = o.Size
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b secretVersion) int { return a.At.Compare(b.At) })
	return out, nil
}

// parseSecretVersion reads a version's key.
func parseSecretVersion(key string) (secretVersion, bool) {
	stamp, ok := strings.CutSuffix(key, ".tar")
	if !ok || strings.Contains(stamp, "/") {
		return secretVersion{}, false
	}
	at, err := time.Parse(secretVersionLayout, stamp)
	if err != nil {
		return secretVersion{}, false
	}
	return secretVersion{Version: key, At: at.UTC()}, true
}

// secretVersionsToDrop are the versions retention lets go: it keeps the
// newest, every one of the last secretFoldersRecent, and the newest of
// each UTC day of the last keepDays days.
func secretVersionsToDrop(versions []secretVersion, now time.Time, keepDays int) []secretVersion {
	if len(versions) <= 1 {
		return nil
	}
	newest := slices.Clone(versions)
	slices.SortFunc(newest, func(a, b secretVersion) int { return b.At.Compare(a.At) })
	horizon := now.UTC().AddDate(0, 0, -keepDays)
	days := map[string]bool{}
	var drop []secretVersion
	for i, v := range newest {
		day := v.At.UTC().Format("2006-01-02")
		switch {
		case i == 0, now.Sub(v.At) < secretFoldersRecent:
			days[day] = true
		case !days[day] && v.At.After(horizon):
			days[day] = true
		default:
			drop = append(drop, v)
		}
	}
	return drop
}

// pruneLocked deletes the versions retention lets go, unless a hold
// waits; the caller holds sf.mu.
func (sf *vaultSecretFolders) pruneLocked(ctx context.Context, now time.Time, keepDays int) {
	versions, err := sf.listVersions(ctx)
	rec := &vaultRecord{At: now}
	if err == nil && sf.state().PruneHold == nil {
		for _, v := range secretVersionsToDrop(versions, now, keepDays) {
			if derr := sf.store.Delete(ctx, v.Version); derr != nil {
				err = derr
				break
			}
			versions = slices.DeleteFunc(versions, func(x secretVersion) bool { return x.Version == v.Version })
		}
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		rec.Error = err.Error()
		sf.s.log.Warn("vault: pruning the secret folders' versions failed", "err", err)
	}
	sf.update(func(st *secretFoldersStatus) {
		st.LastPrune = rec
		if versions != nil || err == nil {
			st.Versions = versions
		}
	})
}

// secretRestoreRequest is POST /api/vault/secret-folders/restore.
type secretRestoreRequest struct {
	// To is a new or empty folder to restore into (default
	// core/restored-secrets-<time> in the data dir).
	To string `json:"to"`
	// At picks the newest version taken at or before it: RFC 3339, or
	// YYYY-MM-DD[THH:MM] in the bridge's timezone (a date alone is the
	// end of that day). Empty is the newest.
	At string `json:"at"`
	// Force restores into the live folders, after copying each to
	// core/replaced-secrets-<time>/.
	Force bool `json:"force"`
}

// RestoreSecretFolders restores a version (POST
// /api/vault/secret-folders/restore, fridolin vault restore-secrets):
// into a new folder, or with Force over the live folders, each copied
// aside first. It waits for a backup that runs.
func (s *vaultService) RestoreSecretFolders(ctx context.Context, req secretRestoreRequest) (secretRestoreRecord, error) {
	_, run, err := s.vault()
	if err != nil {
		return secretRestoreRecord{}, err
	}
	sf := run.secrets
	if sf == nil || sf.store == nil {
		return secretRestoreRecord{}, apperr.New(apperr.Precondition, "the secret folders are not backed up here: "+run.secretsWhyNot())
	}
	cfg := s.cfgs.Get()
	if req.Force && req.To != "" {
		return secretRestoreRecord{}, apperr.New(apperr.Invalid, "--force restores into the live folders: leave out --to")
	}
	at, err := parseSecretAt(req.At, cfg.Location())
	if err != nil {
		return secretRestoreRecord{}, apperr.New(apperr.Invalid, err.Error())
	}
	sf.mu.Lock()
	defer sf.mu.Unlock()
	rec := secretRestoreRecord{At: time.Now().UTC(), Force: req.Force}
	err = sf.restoreLocked(ctx, cfg, req, at, &rec)
	if err != nil {
		rec.Error = err.Error()
		s.log.Warn("vault: restoring the secret folders failed", "version", rec.Version, "err", err)
	} else {
		s.log.Info("vault: restored the secret folders", "version", rec.Version, "to", rec.To, "force", rec.Force, "files", rec.Files, "bytes", rec.Bytes)
	}
	last := rec
	sf.update(func(st *secretFoldersStatus) { st.LastRestore = &last })
	return rec, err
}

// parseSecretAt reads --at; the zero time is the newest version.
func parseSecretAt(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		return t.AddDate(0, 0, 1).Add(-time.Nanosecond), nil
	}
	return time.Time{}, fmt.Errorf("--at %q: a date (2026-10-20), a local time (2026-10-20T14:30) or RFC 3339", s)
}

func (sf *vaultSecretFolders) restoreLocked(ctx context.Context, cfg *config.Settings, req secretRestoreRequest, at time.Time, rec *secretRestoreRecord) error {
	versions, err := sf.listVersions(ctx)
	if err != nil {
		return fmt.Errorf("listing the versions: %w", err)
	}
	var pick *secretVersion
	for i := range versions {
		if at.IsZero() || !versions[i].At.After(at) {
			pick = &versions[i]
		}
	}
	if pick == nil {
		if len(versions) == 0 {
			return apperr.New(apperr.NotFound, "there is no version of the secret folders yet")
		}
		return apperr.New(apperr.NotFound, fmt.Sprintf("no version at or before %s; the oldest is of %s", at.Format(time.RFC3339), versions[0].At.Format(time.RFC3339)))
	}
	rec.Version = pick.Version
	live, err := secretFoldersOf(cfg)
	if err != nil {
		return err
	}
	core := cfg.Boot.Layout().Core()
	stamp := rec.At.Format(secretVersionLayout)
	dest := req.To
	if req.Force || dest == "" {
		if dest, err = newUniqueFolder(filepath.Join(core, "restored-secrets-"+stamp)); err != nil {
			return err
		}
	} else {
		if dest, err = secretRestoreTarget(dest, cfg.Boot.Home, cfg.DataDir); err != nil {
			return err
		}
		for _, f := range live {
			if fsx.Within(fsx.Canonical(dest), fsx.Canonical(f.Path)) || fsx.Within(fsx.Canonical(f.Path), fsx.Canonical(dest)) {
				return apperr.New(apperr.Invalid, fmt.Sprintf("%s is or holds the live %s %s: restore into a new folder, or over the live folders with --force", dest, f.Source, f.Path))
			}
		}
		if err := newEmptyFolder(dest); err != nil {
			return err
		}
	}
	rec.To = dest
	man, files, bytes, err := sf.extract(ctx, pick.Version, dest)
	rec.Folders, rec.Files, rec.Bytes = man.Folders, files, bytes
	if err != nil {
		return fmt.Errorf("restoring %s into %s: %w", pick.Version, dest, err)
	}
	if !req.Force {
		return nil
	}
	// Over the live folders: each is copied aside whole first, then the
	// version's files are written over it, one by one, each through a
	// temp file in its folder; what the version does not have stays.
	for _, mf := range man.Folders {
		i := slices.IndexFunc(live, func(f secretFolder) bool { return f.Path == mf.Path })
		if i < 0 {
			return apperr.New(apperr.Precondition, fmt.Sprintf("the version's folder %s (%s) is not a secret folder here any more: restore it with --to and copy what you need", mf.Path, mf.Source))
		}
	}
	rec.To = "the live folders"
	aside, err := newUniqueFolder(filepath.Join(core, "replaced-secrets-"+stamp))
	if err != nil {
		return err
	}
	rec.Replaced = aside
	for _, mf := range man.Folders {
		if fsx.Exists(mf.Path) {
			if err := copySecretTree(mf.Path, filepath.Join(aside, mf.Label)); err != nil {
				return fmt.Errorf("copying %s aside before the restore (nothing of it was changed): %w", mf.Path, err)
			}
		}
	}
	for _, mf := range man.Folders {
		kept, err := applySecretTree(filepath.Join(dest, mf.Label), mf.Path)
		rec.Kept += kept
		if err != nil {
			return fmt.Errorf("restoring into %s (its files before are in %s): %w", mf.Path, aside, err)
		}
	}
	return os.RemoveAll(dest)
}

// secretRestoreTarget checks --to: absolute, and inside the home folder
// or the data dir (not either itself). The folder comes from the request,
// so it is built again from that base and a part of it that stays inside,
// as restoreTarget does for the Drive backup's restores.
func secretRestoreTarget(to, home, dataDir string) (string, error) {
	if !filepath.IsAbs(to) {
		return "", apperr.New(apperr.Invalid, fmt.Sprintf("--to %q: an absolute path", to))
	}
	target := fsx.Canonical(to)
	for _, base := range []string{home, dataDir} {
		if base == "" {
			continue
		}
		base = fsx.Canonical(AbsPath(base))
		rel, err := filepath.Rel(base, target)
		if err == nil && rel != "." && filepath.IsLocal(rel) {
			return filepath.Join(base, rel), nil
		}
	}
	return "", apperr.New(apperr.Invalid, fmt.Sprintf("--to %s: a new folder inside your home folder or the data dir", to))
}

// newUniqueFolder makes base (0700), or base-2, base-3, … when it is
// taken, and returns the one it made.
func newUniqueFolder(base string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		return "", err
	}
	for n := 1; ; n++ {
		dir := base
		if n > 1 {
			dir += "-" + strconv.Itoa(n)
		}
		err := os.Mkdir(dir, 0o700)
		if err == nil || !errors.Is(err, fs.ErrExist) || n > 100 {
			return dir, err
		}
	}
}

// newEmptyFolder makes dir (0700), or takes it when it is an empty folder.
func newEmptyFolder(dir string) error {
	entries, err := os.ReadDir(dir)
	switch {
	case err == nil && len(entries) > 0:
		return apperr.New(apperr.Conflict, dir+" is not empty: restore into a new or empty folder")
	case err == nil:
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	return os.Mkdir(dir, 0o700)
}

// extract opens version and writes its folders into dest, with their
// files' modes and times; it returns the manifest and what it wrote.
func (sf *vaultSecretFolders) extract(ctx context.Context, version, dest string) (secretManifest, int, int64, error) {
	var man secretManifest
	rc, err := sf.store.Get(ctx, version)
	if err != nil {
		return man, 0, 0, err
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	type dirMode struct {
		path  string
		mode  fs.FileMode
		mtime time.Time
	}
	var dirs []dirMode
	files, bytes := 0, int64(0)
	first := true
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return man, files, bytes, err
		}
		if first {
			first = false
			if h.Name != secretManifestName {
				return man, files, bytes, errors.New("the version does not start with its manifest")
			}
			data, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return man, files, bytes, err
			}
			if err := json.Unmarshal(data, &man); err != nil {
				return man, files, bytes, fmt.Errorf("reading the version's manifest: %w", err)
			}
			continue
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || path.Clean(name) != name || strings.Contains(name, "\\") || !filepath.IsLocal(filepath.FromSlash(name)) {
			return man, files, bytes, fmt.Errorf("the version holds a bad name %q", h.Name)
		}
		label, _, _ := strings.Cut(name, "/")
		if !slices.ContainsFunc(man.Folders, func(f secretFolderView) bool { return f.Label == label }) {
			return man, files, bytes, fmt.Errorf("the version holds %q outside its folders", h.Name)
		}
		p := filepath.Join(dest, filepath.FromSlash(name))
		if !strings.HasPrefix(p, filepath.Clean(dest)+string(filepath.Separator)) {
			return man, files, bytes, fmt.Errorf("the version holds %q outside its folders", h.Name)
		}
		mode := fs.FileMode(h.Mode).Perm()
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o700); err != nil {
				return man, files, bytes, err
			}
			dirs = append(dirs, dirMode{p, mode, h.ModTime})
		case tar.TypeReg:
			if bytes += h.Size; bytes > 2*secretFoldersMaxBytes {
				return man, files, bytes, errors.New("the version is larger than secret folders may be")
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				return man, files, bytes, err
			}
			if err := writeRestoredFile(p, tr, h.Size, mode, h.ModTime, true); err != nil {
				return man, files, bytes, err
			}
			files++
		default:
			return man, files, bytes, fmt.Errorf("the version holds %q of a type it never writes", h.Name)
		}
	}
	if first {
		return man, files, bytes, errors.New("the version is empty")
	}
	// The folders' modes last, the deepest first: a folder of 0500 would
	// refuse its own files.
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i].path, dirs[i].mode); err != nil {
			return man, files, bytes, err
		}
		_ = os.Chtimes(dirs[i].path, dirs[i].mtime, dirs[i].mtime)
	}
	return man, files, bytes, nil
}

// writeRestoredFile writes size bytes of r to p with mode and mtime:
// exclusive refuses a file that is there, else it goes through a temp
// file in p's folder renamed over p.
func writeRestoredFile(p string, r io.Reader, size int64, mode fs.FileMode, mtime time.Time, exclusive bool) error {
	target := p
	var f *os.File
	var err error
	if exclusive {
		f, err = os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	} else {
		f, err = os.CreateTemp(filepath.Dir(p), ".fridolin-restore-*")
		if err == nil {
			p = f.Name()
			defer os.Remove(p)
		}
	}
	if err != nil {
		return err
	}
	if _, err := io.CopyN(f, r, size); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		return err
	}
	if !exclusive {
		return os.Rename(p, target)
	}
	return nil
}

// copySecretTree copies the folder src to dst, which must not exist,
// with modes and times, and checks each copy's size: the copy a forced
// restore keeps of what it replaces. Symbolic links are left out, as a
// version leaves them out.
func copySecretTree(src, dst string) error {
	root, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	type dirMode struct {
		path string
		fi   fs.FileInfo
	}
	var dirs []dirMode
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		switch {
		case fi.IsDir():
			if rel == "." {
				if err := os.Mkdir(out, 0o700); err != nil {
					return err
				}
			} else if err := os.MkdirAll(out, 0o700); err != nil {
				return err
			}
			dirs = append(dirs, dirMode{out, fi})
		case fi.Mode().IsRegular():
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			err = writeRestoredFile(out, f, fi.Size(), fi.Mode().Perm(), fi.ModTime(), true)
			f.Close()
			if err != nil {
				return err
			}
			if got, err := os.Stat(out); err != nil || got.Size() != fi.Size() {
				return fmt.Errorf("the copy of %s is not whole", p)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i].path, dirs[i].fi.Mode().Perm()); err != nil {
			return err
		}
		_ = os.Chtimes(dirs[i].path, dirs[i].fi.ModTime(), dirs[i].fi.ModTime())
	}
	return nil
}

// applySecretTree writes the restored folder src over the live folder
// dst: each file through a temp file renamed over its name, folders made
// as needed with their modes. It returns how many files of dst the
// version does not have, which stay.
func applySecretTree(src, dst string) (int, error) {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return 0, err
	}
	have := map[string]bool{}
	type dirMode struct {
		path string
		fi   fs.FileInfo
	}
	var dirs []dirMode
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		have[rel] = true
		if fi.IsDir() {
			if err := os.MkdirAll(out, 0o700); err != nil {
				return err
			}
			// Writable while the files go in; its own mode at the end.
			if err := os.Chmod(out, 0o700); err != nil {
				return err
			}
			dirs = append(dirs, dirMode{out, fi})
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		return writeRestoredFile(out, f, fi.Size(), fi.Mode().Perm(), fi.ModTime(), false)
	})
	kept := 0
	if werr := filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rel, rerr := filepath.Rel(dst, p); rerr == nil && !d.IsDir() && !have[rel] {
			kept++
		}
		return nil
	}); err == nil {
		err = werr
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if cerr := os.Chmod(dirs[i].path, dirs[i].fi.Mode().Perm()); cerr != nil && err == nil {
			err = cerr
		}
		_ = os.Chtimes(dirs[i].path, dirs[i].fi.ModTime(), dirs[i].fi.ModTime())
	}
	return kept, err
}
