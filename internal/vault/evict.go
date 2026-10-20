package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Eviction is one file sent cold.
type Eviction struct {
	Path   string    `json:"path"`
	Size   int64     `json:"size"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// EvictReport says what one Evict did.
type EvictReport struct {
	HotBytesBefore int64             `json:"hot_bytes_before"`
	HotBytesAfter  int64             `json:"hot_bytes_after"`
	TargetBytes    int64             `json:"target_bytes"` // 0: no budget
	Evicted        []Eviction        `json:"evicted,omitempty"`
	NotUploaded    []string          `json:"not_uploaded,omitempty"` // eligible, but cold is not complete yet
	Failed         map[string]string `json:"failed,omitempty"`
	// OverBudget: hot usage is still above the target, because what is
	// left is pinned, held, or not uploaded yet.
	OverBudget bool `json:"over_budget"`
}

func (e *EvictReport) fail(rel string, err error) {
	if e.Failed == nil {
		e.Failed = map[string]string{}
	}
	e.Failed[rel] = err.Error()
}

// targetBytes is the hot usage eviction aims for: the budget less the
// headroom. 0 means no budget.
func (v *Vault) targetBytes() int64 {
	if v.opts.BudgetBytes <= 0 {
		return 0
	}
	return v.opts.BudgetBytes * int64(100-v.opts.HeadroomPercent) / 100
}

// HotBytes is the size of every file the index has on disk.
func (v *Vault) HotBytes(ctx context.Context) (int64, error) {
	var n int64
	err := v.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM files WHERE tier IN ('hot', 'both')`).Scan(&n)
	return n, err
}

// Reclassify runs the policy over every row and stores the classes. Evict
// does it first; the dashboard may call it to show current classes.
func (v *Vault) Reclassify(ctx context.Context) error {
	_, err := v.reclassify(ctx)
	return err
}

func (v *Vault) reclassify(ctx context.Context) ([]row, error) {
	threads := map[string]bool{}
	if v.opts.OpenThreadPaths != nil {
		paths, err := v.opts.OpenThreadPaths(ctx)
		if err != nil {
			return nil, fmt.Errorf("vault: open thread paths: %w", err)
		}
		for _, p := range paths {
			if p, err := cleanStrict(p); err == nil {
				threads[p] = true
			}
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	rows, err := queryRows(ctx, v.db, ``)
	if err != nil {
		return nil, err
	}
	exists := make(map[string]bool, len(rows))
	for _, r := range rows {
		exists[r.Path] = true
	}
	now := v.now()
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		r := &rows[i]
		companion := false
		if IsBinary(r.Path) {
			for _, c := range v.policy.companions(r.Path) {
				if c != r.Path && exists[c] {
					companion = true
					break
				}
			}
		}
		d := v.policy.Classify(Facts{
			Path: r.Path, LastAccess: fromMS(r.LastAccess), HydratedAt: fromMS(r.HydratedAt),
			HasCompanion: companion, InOpenThread: threads[r.Path],
		}, now)
		if d.Class == r.Class && d.Pinned == r.Pinned && d.Reason == r.ClassReason && companion == r.HasCompanion {
			continue
		}
		r.Class, r.Pinned, r.ClassReason, r.HasCompanion = d.Class, d.Pinned, d.Reason, companion
		if _, err := tx.ExecContext(ctx, `UPDATE files SET class = ?, pinned = ?, class_reason = ?, has_companion = ? WHERE path = ?`,
			r.Class, r.Pinned, r.ClassReason, r.HasCompanion, r.Path); err != nil {
			return nil, errors.Join(err, tx.Rollback())
		}
	}
	return rows, tx.Commit()
}

// Evict sends files cold. Every cold-eligible file goes, least recently
// used first (cold-eligible means nothing keeps it hot any more); then,
// while hot usage is above the budget less its headroom, files that are
// active only because they were used recently go too, again oldest first.
// Pinned files, files of open threads, recently hydrated files and
// binaries held by the markdown-first rule always stay.
//
// A file is deleted from disk only after it is shown to be in the cold
// tier: its row is tier both, the hash of the bytes on disk is the hash
// of the bytes uploaded, and the cold object has their size (with
// VerifyByDownload, the cold bytes are hashed too).
func (v *Vault) Evict(ctx context.Context) (EvictReport, error) {
	v.jobMu.Lock()
	defer v.jobMu.Unlock()
	rows, err := v.reclassify(ctx)
	if err != nil {
		return EvictReport{}, err
	}
	rep := EvictReport{TargetBytes: v.targetBytes()}
	if rep.HotBytesBefore, err = v.HotBytes(ctx); err != nil {
		return rep, err
	}
	hot := rep.HotBytesBefore
	lru := func(sel func(row) bool) []row {
		var out []row
		for _, r := range rows {
			if sel(r) {
				out = append(out, r)
			}
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].LastAccess != out[j].LastAccess {
				return out[i].LastAccess < out[j].LastAccess
			}
			return out[i].Path < out[j].Path
		})
		return out
	}
	evict := func(r row, reason string) error {
		if r.Tier != TierBoth {
			rep.NotUploaded = append(rep.NotUploaded, r.Path)
			return nil
		}
		if err := v.evictOne(ctx, r, reason); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			rep.fail(r.Path, err)
			return nil
		}
		hot -= r.Size
		rep.Evicted = append(rep.Evicted, Eviction{Path: r.Path, Size: r.Size, Reason: reason, At: v.now().UTC()})
		return nil
	}
	for _, r := range lru(func(r row) bool { return r.Class == ClassColdEligible && r.Tier != TierCold }) {
		if err := evict(r, r.ClassReason); err != nil {
			return rep, err
		}
	}
	if rep.TargetBytes > 0 && hot > rep.TargetBytes {
		for _, r := range lru(func(r row) bool {
			return r.Class == ClassActive && r.ClassReason == ReasonRecent && r.Tier != TierCold
		}) {
			if hot <= rep.TargetBytes {
				break
			}
			if err := evict(r, "budget"); err != nil {
				return rep, err
			}
		}
	}
	rep.HotBytesAfter, err = v.HotBytes(ctx)
	rep.OverBudget = rep.TargetBytes > 0 && rep.HotBytesAfter > rep.TargetBytes
	return rep, err
}

// evictTemp marks the name a file is moved to while its eviction checks
// it. New puts back any such file a crash left.
const evictTemp = "evict"

// evictOne evicts one file: it moves the file aside (so nothing writes to
// it through its path meanwhile), proves the cold copy, lists it in
// .cold.json, deletes it and records tier cold. Any doubt puts the file
// back.
func (v *Vault) evictOne(ctx context.Context, r row, reason string) error {
	if r.ColdSHA256 == "" || r.SHA256 != r.ColdSHA256 {
		return errors.New("cold hash does not match the hot hash")
	}
	info, err := v.opts.Store.Stat(ctx, r.ColdKey)
	if err != nil {
		return fmt.Errorf("checking the cold copy: %w", err)
	}
	if info.Size != r.Size {
		return fmt.Errorf("cold copy has %d bytes, hot %d", info.Size, r.Size)
	}
	if v.opts.VerifyByDownload {
		if err := v.verifyCold(ctx, r); err != nil {
			return err
		}
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	cur, ok, err := getRow(ctx, v.db, r.Path)
	if err != nil || !ok || cur.Tier != TierBoth || cur.SHA256 != r.SHA256 {
		return errors.New("changed since the eviction began")
	}
	fi, err := v.lstat(r.Path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Size() != r.Size || fi.ModTime().UnixNano() != r.MtimeNS {
		return errors.New("changed on disk since its upload")
	}
	aside := tempName(r.Path, evictTemp)
	if err := v.root.Rename(filepath.FromSlash(r.Path), filepath.FromSlash(aside)); err != nil {
		return err
	}
	putBack := func(cause error) error {
		if err := v.root.Rename(filepath.FromSlash(aside), filepath.FromSlash(r.Path)); err != nil {
			return errors.Join(cause, fmt.Errorf("putting the file back from %s: %w", aside, err))
		}
		return cause
	}
	sum, n, err := v.hashFile(aside)
	if err != nil {
		return putBack(err)
	}
	if sum != r.ColdSHA256 || n != r.Size {
		return putBack(errors.New("hot bytes differ from the cold copy"))
	}
	if err := v.addColdListing(cur); err != nil {
		return putBack(err)
	}
	if err := v.root.Remove(filepath.FromSlash(aside)); err != nil {
		return errors.Join(err, v.removeColdListing(r.Path), v.root.Rename(filepath.FromSlash(aside), filepath.FromSlash(r.Path)))
	}
	cur.Tier = TierCold
	cur.UpdatedAt = ms(v.now())
	if err := putRow(ctx, v.db, cur); err != nil {
		return err
	}
	_, err = v.db.ExecContext(ctx, `INSERT INTO evictions (path, size, reason, at) VALUES (?, ?, ?, ?)`,
		r.Path, r.Size, reason, ms(v.now()))
	return err
}

// verifyCold downloads the cold copy of r and compares its hash.
func (v *Vault) verifyCold(ctx context.Context, r row) error {
	rc, err := v.opts.Store.Get(ctx, r.ColdKey)
	if err != nil {
		return fmt.Errorf("downloading the cold copy: %w", err)
	}
	defer func() { _ = rc.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, rc); err != nil {
		return fmt.Errorf("downloading the cold copy: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != r.ColdSHA256 {
		return &HashMismatchError{Path: r.Path, Want: r.ColdSHA256, Got: got}
	}
	return nil
}

// EvictionLog returns the last limit evictions, newest first.
func (v *Vault) EvictionLog(ctx context.Context, limit int) ([]Eviction, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := v.db.QueryContext(ctx, `SELECT path, size, reason, at FROM evictions ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Eviction
	for rows.Next() {
		var e Eviction
		var at int64
		if err := rows.Scan(&e.Path, &e.Size, &e.Reason, &at); err != nil {
			return nil, err
		}
		e.At = fromMS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// recoverEvictions puts back files an interrupted eviction moved aside,
// and removes downloads an interrupted hydration left.
func (v *Vault) recoverEvictions() error {
	var aside, partial []string
	err := fs.WalkDir(v.root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && (d.Name() == ".fridolin-vault") {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		switch {
		case strings.Contains(name, ".vault-"+evictTemp+"-") && strings.HasSuffix(name, ".tmp"):
			aside = append(aside, filepath.ToSlash(p))
		case strings.Contains(name, ".vault-"+hydrateTemp+"-") && strings.HasSuffix(name, ".tmp"):
			partial = append(partial, filepath.ToSlash(p))
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, p := range partial {
		_ = v.root.Remove(filepath.FromSlash(p))
	}
	for _, p := range aside {
		orig, ok := originalOfTemp(p, evictTemp)
		if !ok {
			continue
		}
		if _, err := v.lstat(orig); err == nil {
			v.log.Warn("vault: a file set aside by an interrupted eviction is kept beside a newer one", "path", orig, "aside", p)
			continue
		}
		if err := v.root.Rename(filepath.FromSlash(p), filepath.FromSlash(orig)); err != nil {
			return err
		}
		if err := v.removeColdListing(orig); err != nil {
			return err
		}
		v.log.Info("vault: put back a file an interrupted eviction set aside", "path", orig)
	}
	return nil
}

// originalOfTemp undoes tempName: dir/.name.vault-kind-xxxx.tmp → dir/name.
func originalOfTemp(p, kind string) (string, bool) {
	dir, name := path.Split(p)
	name, ok := strings.CutPrefix(name, ".")
	if !ok {
		return "", false
	}
	i := strings.LastIndex(name, ".vault-"+kind+"-")
	if i <= 0 {
		return "", false
	}
	return dir + name[:i], true
}
