package vault

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"time"
)

var randReader = rand.Reader

// SyncReport says what one Sync did.
type SyncReport struct {
	Uploaded      []string          `json:"uploaded,omitempty"`       // now tier both
	UploadedBytes int64             `json:"uploaded_bytes"`           //
	Failed        map[string]string `json:"failed,omitempty"`         // path → error; still hot
	Forgotten     []string          `json:"forgotten,omitempty"`      // never uploaded and gone from disk: row dropped
	ColdOnly      []string          `json:"cold_only,omitempty"`      // gone from disk but in cold: now tier cold
	Reconciled    []string          `json:"reconciled,omitempty"`     // back on disk with the cold hash: tier both
	Skipped       int               `json:"skipped"`                  // unchanged
	ManifestError string            `json:"manifest_error,omitempty"` //
}

func (s *SyncReport) fail(rel string, err error) {
	if s.Failed == nil {
		s.Failed = map[string]string{}
	}
	s.Failed[rel] = err.Error()
}

func (s *SyncReport) changed() bool {
	return len(s.Uploaded)+len(s.Forgotten)+len(s.ColdOnly)+len(s.Reconciled) > 0
}

// Sync walks the whole hot tier and brings the cold tier up to date: every
// new or changed file is hashed, uploaded under its relative path, checked
// by the object's size and marked tier both. A file that vanished from
// disk keeps its cold copy (tier cold, listed in .cold.json); deleting
// data is never a side effect of a sync.
func (v *Vault) Sync(ctx context.Context) (SyncReport, error) {
	v.jobMu.Lock()
	defer v.jobMu.Unlock()
	hot, err := v.walkHot()
	if err != nil {
		return SyncReport{}, err
	}
	rows, err := queryRows(ctx, v.db, ``)
	if err != nil {
		return SyncReport{}, err
	}
	paths := make([]string, 0, len(hot)+len(rows))
	for rel := range hot {
		paths = append(paths, rel)
	}
	for _, r := range rows {
		if _, ok := hot[r.Path]; !ok {
			paths = append(paths, r.Path)
		}
	}
	sort.Strings(paths)
	return v.syncLocked(ctx, paths, nil)
}

// SyncPaths is Sync for the given paths only (what the watcher saw
// change). A path that is a removed folder covers the rows below it.
func (v *Vault) SyncPaths(ctx context.Context, rels []string) (SyncReport, error) {
	return v.syncPaths(ctx, rels, nil)
}

func (v *Vault) syncPaths(ctx context.Context, rels []string, prov map[string]Provenance) (SyncReport, error) {
	v.jobMu.Lock()
	defer v.jobMu.Unlock()
	seen := map[string]bool{}
	var paths []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	for _, rel := range rels {
		rel, err := cleanStrict(rel)
		if err != nil || v.ignored(rel) {
			continue
		}
		fi, err := v.lstat(rel)
		switch {
		case err == nil && fi.IsDir():
			// A folder that appeared (or moved in): sync what is inside.
			if err := fs.WalkDir(v.root.FS(), filepath.FromSlash(rel), v.walkFunc(func(p string, _ fs.FileInfo) { add(p) })); err != nil {
				return SyncReport{}, err
			}
			continue
		case err == nil:
			add(rel)
			continue
		}
		add(rel)
		// Gone: it may have been a folder; take the rows below it too.
		below, err := queryRows(ctx, v.db, `WHERE path LIKE ? ESCAPE '\'`, likeEscape(rel)+"/%")
		if err != nil {
			return SyncReport{}, err
		}
		for _, r := range below {
			add(r.Path)
		}
	}
	sort.Strings(paths)
	return v.syncLocked(ctx, paths, prov)
}

// walkFunc adapts fn to fs.WalkDir over v.root.FS(), skipping what the
// vault ignores and anything that is not a regular file.
func (v *Vault) walkFunc(fn func(rel string, fi fs.FileInfo)) fs.WalkDirFunc {
	return func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		rel := filepath.ToSlash(p)
		if rel == "." {
			return nil
		}
		if v.ignored(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		fn(rel, fi)
		return nil
	}
}

// walkHot lists every vault file on disk.
func (v *Vault) walkHot() (map[string]fs.FileInfo, error) {
	out := map[string]fs.FileInfo{}
	err := fs.WalkDir(v.root.FS(), ".", v.walkFunc(func(rel string, fi fs.FileInfo) { out[rel] = fi }))
	return out, err
}

// syncLocked syncs paths; the caller holds jobMu.
func (v *Vault) syncLocked(ctx context.Context, paths []string, prov map[string]Provenance) (SyncReport, error) {
	var rep SyncReport
	for _, rel := range paths {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if err := v.syncOne(ctx, rel, prov[rel], &rep); err != nil {
			return rep, err
		}
	}
	if rep.changed() {
		v.manifestDue = true
	}
	if v.manifestDue && v.now().Sub(v.manifestAt) >= v.opts.ManifestEvery {
		if err := v.flushManifestLocked(ctx); err != nil {
			rep.ManifestError = err.Error()
		}
	}
	return rep, nil
}

// syncOne brings one path up to date. Errors of the file itself go into
// rep; the returned error is for the index failing.
func (v *Vault) syncOne(ctx context.Context, rel string, prov Provenance, rep *SyncReport) error {
	r, known, err := getRow(ctx, v.db, rel)
	if err != nil {
		return err
	}
	fi, err := v.lstat(rel)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		rep.fail(rel, err)
		return nil
	}
	if err != nil || !fi.Mode().IsRegular() {
		return v.syncGone(ctx, rel, r, known, rep)
	}
	size, mtime := fi.Size(), fi.ModTime().UnixNano()
	if known && r.Size == size && r.MtimeNS == mtime && prov == "" {
		switch r.Tier {
		case TierBoth:
			rep.Skipped++
			return nil
		case TierCold:
			// Back on disk with the size and time it had: an eviction
			// that stopped after deleting, or a copy put back. Same bytes
			// as cold? Then it is simply hot again.
			if sum, _, err := v.hashFile(rel); err == nil && sum == r.ColdSHA256 {
				return v.reconcile(ctx, r, rep)
			}
		case TierHot:
			// Not uploaded yet, or changed since: uploaded below.
		}
	}
	return v.upload(ctx, rel, r, known, prov, rep)
}

// syncGone handles a path no longer on disk.
func (v *Vault) syncGone(ctx context.Context, rel string, r row, known bool, rep *SyncReport) error {
	if !known {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, err := v.lstat(rel); err == nil {
		return nil // came back meanwhile; the next sync sees it
	}
	switch {
	case r.Tier == TierHot && r.ColdSHA256 == "":
		// Never reached the cold tier: nothing is left to keep.
		if err := deleteRow(ctx, v.db, rel); err != nil {
			return err
		}
		rep.Forgotten = append(rep.Forgotten, rel)
	case r.Tier == TierBoth || (r.Tier == TierHot && r.ColdSHA256 != ""):
		// Cold keeps the last uploaded version. For tier hot that is an
		// older one than the file deleted, which is still better than
		// nothing; the row and .cold.json describe what cold holds.
		r.Tier = TierCold
		if r.SHA256 != r.ColdSHA256 {
			r.SHA256 = r.ColdSHA256
			if info, err := v.opts.Store.Stat(ctx, r.ColdKey); err == nil {
				r.Size = info.Size
			}
		}
		r.UpdatedAt = ms(v.now())
		if err := v.addColdListing(r); err != nil {
			rep.fail(rel, err)
			return nil
		}
		if err := putRow(ctx, v.db, r); err != nil {
			return err
		}
		rep.ColdOnly = append(rep.ColdOnly, rel)
	}
	return nil
}

func (v *Vault) reconcile(ctx context.Context, r row, rep *SyncReport) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	r.Tier = TierBoth
	r.SHA256 = r.ColdSHA256
	r.UpdatedAt = ms(v.now())
	if err := v.removeColdListing(r.Path); err != nil {
		rep.fail(r.Path, err)
	}
	if err := putRow(ctx, v.db, r); err != nil {
		return err
	}
	rep.Reconciled = append(rep.Reconciled, r.Path)
	return nil
}

// upload sends rel to the cold tier. The row goes to tier hot first, so
// that a crash part way never leaves an older cold copy looking current,
// and once Put returned it names the bytes Put sent (Store.Put replaces
// an object whole or not at all).
func (v *Vault) upload(ctx context.Context, rel string, r row, known bool, prov Provenance, rep *SyncReport) error {
	now := v.now()
	f, err := v.root.Open(filepath.FromSlash(rel))
	if err != nil {
		rep.fail(rel, err)
		return nil
	}
	defer func() { _ = f.Close() }()
	before, err := f.Stat()
	if err != nil {
		rep.fail(rel, err)
		return nil
	}
	wasCold := known && r.Tier == TierCold
	if !known {
		r = row{Path: rel, Provenance: v.provenance(rel), Class: ClassActive}
	}
	if prov != "" {
		r.Provenance = prov
	}
	r.Size = before.Size()
	r.MtimeNS = before.ModTime().UnixNano()
	r.SHA256 = ""
	r.Tier = TierHot
	r.LastAccess = max(r.LastAccess, ms(before.ModTime()))
	r.UpdatedAt = ms(now)
	if wasCold {
		// A new file where an evicted one was: the new bytes win, and the
		// old cold copy stays as an earlier object version.
		if err := v.removeColdListing(rel); err != nil {
			rep.fail(rel, err)
		}
	}
	if known && r.ColdSHA256 != "" && r.ColdKey != "" {
		// Touched but perhaps not changed: the same bytes need no upload.
		if sum, n, err := v.hashFile(rel); err == nil && sum == r.ColdSHA256 && n == r.Size {
			if info, err := v.opts.Store.Stat(ctx, r.ColdKey); err == nil && info.Size == n {
				v.mu.Lock()
				defer v.mu.Unlock()
				if after, err := v.lstat(rel); err == nil && after.Size() == n && after.ModTime().UnixNano() == r.MtimeNS {
					r.SHA256, r.Tier = sum, TierBoth
					if err := putRow(ctx, v.db, r); err != nil {
						return err
					}
					rep.Reconciled = append(rep.Reconciled, rel)
				}
				return nil
			}
		}
	}
	v.mu.Lock()
	err = putRow(ctx, v.db, r)
	v.mu.Unlock()
	if err != nil {
		return err
	}

	h := sha256.New()
	counter := &countingWriter{}
	key := rel
	if err := v.opts.Store.Put(ctx, key, io.TeeReader(f, io.MultiWriter(h, counter))); err != nil {
		rep.fail(rel, fmt.Errorf("upload: %w", err))
		return nil
	}
	sum := hex.EncodeToString(h.Sum(nil))
	// Put replaced the cold object, so from here the row names what went
	// up, even when a check below fails: the old hash no longer holds.
	r.ColdKey, r.ColdSHA256 = key, sum
	info, err := v.opts.Store.Stat(ctx, key)
	if err == nil && info.Size != counter.n {
		err = fmt.Errorf("cold copy has %d bytes, uploaded %d", info.Size, counter.n)
		r.ColdKey, r.ColdSHA256 = "", "" // cold holds bytes nobody hashed
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err != nil {
		rep.fail(rel, fmt.Errorf("verify: %w", err))
		return putRow(ctx, v.db, r)
	}
	after, err := v.lstat(rel)
	if err != nil || after.Size() != counter.n || after.ModTime().UnixNano() != r.MtimeNS || !after.Mode().IsRegular() {
		// Written to while uploading: what went up may be torn. Stay hot;
		// the change brings another sync.
		rep.fail(rel, errors.New("changed during upload; retried on the next sync"))
		return putRow(ctx, v.db, r)
	}
	r.SHA256 = sum
	r.Size = counter.n
	r.Tier = TierBoth
	r.UploadedAt = ms(v.now())
	r.UpdatedAt = r.UploadedAt
	if err := putRow(ctx, v.db, r); err != nil {
		return err
	}
	rep.Uploaded = append(rep.Uploaded, rel)
	rep.UploadedBytes += counter.n
	return nil
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// manifest is the cold tier's own index: every file whose bytes are in the
// cold tier, with the hash of those bytes.
type manifest struct {
	Version int             `json:"version"`
	Written time.Time       `json:"written"`
	Files   []manifestEntry `json:"files"`
}

type manifestEntry struct {
	Path       string     `json:"path"`
	Size       int64      `json:"size"`
	MtimeNS    int64      `json:"mtime_ns"`
	SHA256     string     `json:"sha256"`
	Provenance Provenance `json:"provenance"`
}

// FlushManifest writes the cold manifest when a change waits for it and
// ManifestEvery has passed since the last write, or at once with force
// (the vault is stopping). It does nothing while a job runs, unless
// force, which waits for it.
func (v *Vault) FlushManifest(ctx context.Context, force bool) error {
	if force {
		v.jobMu.Lock()
	} else if !v.jobMu.TryLock() {
		return nil
	}
	defer v.jobMu.Unlock()
	if !v.manifestDue || (!force && v.now().Sub(v.manifestAt) < v.opts.ManifestEvery) {
		return nil
	}
	return v.flushManifestLocked(ctx)
}

// flushManifestLocked writes the manifest; the caller holds jobMu.
func (v *Vault) flushManifestLocked(ctx context.Context) error {
	if err := v.writeManifest(ctx); err != nil {
		v.log.Warn("vault: writing the cold manifest failed", "err", err)
		return err
	}
	v.manifestAt, v.manifestDue = v.now(), false
	return nil
}

func (v *Vault) writeManifest(ctx context.Context) error {
	rows, err := queryRows(ctx, v.db, `WHERE cold_sha256 != '' ORDER BY path`)
	if err != nil {
		return err
	}
	m := manifest{Version: 1, Written: v.now().UTC(), Files: make([]manifestEntry, 0, len(rows))}
	for _, r := range rows {
		e := manifestEntry{Path: r.Path, Size: r.Size, MtimeNS: r.MtimeNS, SHA256: r.ColdSHA256, Provenance: r.Provenance}
		m.Files = append(m.Files, e)
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return v.opts.Store.Put(ctx, manifestKey, bytes.NewReader(data))
}

func (v *Vault) readManifest(ctx context.Context) (map[string]manifestEntry, error) {
	rc, err := v.opts.Store.Get(ctx, manifestKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	var m manifest
	if err := json.NewDecoder(rc).Decode(&m); err != nil {
		return nil, fmt.Errorf("vault: reading the cold manifest: %w", err)
	}
	out := make(map[string]manifestEntry, len(m.Files))
	for _, e := range m.Files {
		out[e.Path] = e
	}
	return out, nil
}

// dirOf is the folder of a vault path, "" for the root.
func dirOf(rel string) string {
	d := path.Dir(rel)
	if d == "." {
		return ""
	}
	return d
}
