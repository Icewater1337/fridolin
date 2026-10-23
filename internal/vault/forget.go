package vault

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// forgetBatch is how many files Forget drops from the index in one
// transaction: an interrupted forget keeps what it finished, and the next
// one goes on from there.
const forgetBatch = 200

// ForgetReport says what one Forget did.
type ForgetReport struct {
	Prefix string `json:"prefix"`
	// Forgotten counts the files whose row and cold copy are gone, Bytes
	// their size as the index had it.
	Forgotten int   `json:"forgotten"`
	Bytes     int64 `json:"bytes"`
	// Orphans counts cold objects below the prefix that no row named,
	// deleted as well.
	Orphans int `json:"orphans,omitempty"`
	// Kept are the paths below the prefix that are on disk: Forget leaves
	// them, and the vault goes on keeping them.
	Kept   []string          `json:"kept,omitempty"`
	Failed map[string]string `json:"failed,omitempty"`
}

func (r *ForgetReport) fail(rel string, err error) {
	if r.Failed == nil {
		r.Failed = map[string]string{}
	}
	r.Failed[rel] = err.Error()
}

// Forget drops what the vault holds below prefix, a folder that is no
// longer part of the hot root (a brain folder a core move had in the data
// dir and brought home again, docs/VAULT.md "Forgetting a folder"): each
// file's cold copy is deleted, then its row, in batches, and the folder's
// .cold.json listings go. A file below prefix that is on disk stays,
// row, cold copy and all, and is reported in Kept: Forget never takes the
// only copy of something. Cold objects below prefix that no row names go
// too. The manifest is written again afterwards.
func (v *Vault) Forget(ctx context.Context, prefix string) (ForgetReport, error) {
	prefix = strings.TrimSuffix(prefix, "/")
	rep := ForgetReport{Prefix: prefix}
	if _, err := cleanStrict(prefix); err != nil {
		return rep, err
	}
	if own := path.Dir(manifestKey); prefix == own || strings.HasPrefix(prefix, own+"/") || strings.HasPrefix(own, prefix+"/") {
		return rep, fmt.Errorf("vault: %s holds the vault's own manifest", prefix)
	}
	v.jobMu.Lock()
	defer v.jobMu.Unlock()
	rows, err := queryRows(ctx, v.db, `WHERE path = ? OR path LIKE ? ESCAPE '\' ORDER BY path`, prefix, likeEscape(prefix)+"/%")
	if err != nil {
		return rep, err
	}
	named := map[string]bool{}
	dirs := map[string]bool{}
	var gone []row
	for _, r := range rows {
		named[r.ColdKey] = true
		named[r.Path] = true
		if v.onDisk(r.Path) {
			rep.Kept = append(rep.Kept, r.Path)
			continue
		}
		gone = append(gone, r)
		dirs[dirOf(r.Path)] = true
	}
	for len(gone) > 0 {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		n := min(forgetBatch, len(gone))
		if err := v.forgetBatch(ctx, gone[:n], &rep); err != nil {
			return rep, err
		}
		gone = gone[n:]
	}
	// Objects no row names: an earlier forget that stopped between the
	// object and the row, or an index rebuilt without them.
	objects, err := v.opts.Store.List(ctx, prefix)
	if err != nil {
		return rep, fmt.Errorf("vault: listing %s in the cold tier: %w", prefix, err)
	}
	for _, o := range objects {
		if named[o.Key] || o.Key == manifestKey || v.onDisk(o.Key) {
			continue
		}
		if err := v.opts.Store.Delete(ctx, o.Key); err != nil {
			rep.fail(o.Key, err)
			continue
		}
		rep.Orphans++
	}
	if err := v.dropColdListings(prefix, dirs); err != nil {
		v.log.Warn("vault: removing the cold listings below a forgotten folder", "prefix", prefix, "err", err)
	}
	if rep.Forgotten+rep.Orphans > 0 {
		v.manifestDue = true
		if err := v.flushManifestLocked(ctx); err != nil {
			rep.fail(manifestKey, err)
		}
	}
	return rep, nil
}

// onDisk reports whether rel is a regular file in the hot tier.
func (v *Vault) onDisk(rel string) bool {
	fi, err := v.lstat(rel)
	return err == nil && fi.Mode().IsRegular()
}

// forgetBatch deletes the cold copies of rows, then drops the rows whose
// copy went, in one transaction.
func (v *Vault) forgetBatch(ctx context.Context, rows []row, rep *ForgetReport) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var done []row
	for _, r := range rows {
		if v.onDisk(r.Path) {
			rep.Kept = append(rep.Kept, r.Path) // came back meanwhile
			continue
		}
		if r.ColdKey != "" {
			if err := v.opts.Store.Delete(ctx, r.ColdKey); err != nil {
				rep.fail(r.Path, err)
				continue
			}
		}
		if err := deleteRow(ctx, tx, r.Path); err != nil {
			return errors.Join(err, tx.Rollback())
		}
		done = append(done, r)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, r := range done {
		rep.Forgotten++
		rep.Bytes += r.Size
	}
	return nil
}

// dropColdListings removes every .cold.json at or below prefix, and the
// folders that leaves empty, deepest first. A folder with anything else
// in it stays.
func (v *Vault) dropColdListings(prefix string, dirs map[string]bool) error {
	var errs []error
	err := fs.WalkDir(v.root.FS(), filepath.FromSlash(prefix), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			dirs[filepath.ToSlash(p)] = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	list := make([]string, 0, len(dirs))
	for d := range dirs {
		if d == prefix || strings.HasPrefix(d, prefix+"/") {
			list = append(list, d)
		}
	}
	// Deepest first, so a parent is empty by the time it comes.
	sort.Slice(list, func(i, j int) bool { return strings.Count(list[i], "/") > strings.Count(list[j], "/") })
	for _, d := range list {
		if err := v.writeColdListing(d, nil); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		_ = v.root.Remove(filepath.FromSlash(d)) // only an empty folder goes
	}
	return errors.Join(errs...)
}
