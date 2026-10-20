package vault

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
)

// RebuildReport says what Rebuild found.
type RebuildReport struct {
	Rows int `json:"rows"`
	Hot  int `json:"hot"`  // on disk only (or the cold copy is stale or unverifiable)
	Cold int `json:"cold"` // in cold only
	Both int `json:"both"` // on disk with a verified cold copy
	// UnknownHash are cold files whose hash neither the cold manifest nor
	// a .cold.json knew; Get accepts their bytes as they come.
	UnknownHash []string `json:"unknown_hash,omitempty"`
	// PinnedCold are pinned files that are only cold; HydratePinned
	// brings them back.
	PinnedCold []string `json:"pinned_cold,omitempty"`
	// ManifestMissing: the cold tier had no manifest (or an unreadable one).
	ManifestMissing bool `json:"manifest_missing"`
}

// Rebuild reconstructs vault.db from the cold listing and a walk of the
// hot tier, as after losing vault.db or on a fresh core. Hashes come from
// the cold manifest (written by every Sync), else from the .cold.json
// files on disk, else from hashing the hot file; a cold copy is trusted to
// be the hot file's only when their hashes agree. Afterwards every
// .cold.json matches the index. The index is replaced in one transaction.
func (v *Vault) Rebuild(ctx context.Context) (RebuildReport, error) {
	v.jobMu.Lock()
	defer v.jobMu.Unlock()
	var rep RebuildReport
	objects, err := v.opts.Store.List(ctx, "")
	if err != nil {
		return rep, fmt.Errorf("vault: listing the cold tier: %w", err)
	}
	man, err := v.readManifest(ctx)
	if err != nil {
		if !errors.Is(err, cloud.ErrNotFound) {
			v.log.Warn("vault: the cold manifest is unreadable; rebuilding without it", "err", err)
		}
		rep.ManifestMissing = true
		man = map[string]manifestEntry{}
	}
	hot, err := v.walkHot()
	if err != nil {
		return rep, err
	}
	listed := map[string]ColdFile{} // from .cold.json files on disk
	dirs := map[string]bool{}
	for rel := range hot {
		dirs[dirOf(rel)] = true
	}
	for _, o := range objects {
		dirs[dirOf(o.Key)] = true
	}
	for dir := range dirs {
		l, err := v.ReadColdListing(dir)
		if err != nil {
			continue
		}
		for _, f := range l.Files {
			listed[path.Join(dir, f.Name)] = f
		}
	}

	now := v.now()
	rows := map[string]row{}
	for _, o := range objects {
		if o.Key == manifestKey || v.ignored(o.Key) {
			continue
		}
		r := row{Path: o.Key, Size: o.Size, MtimeNS: o.ModTime.UnixNano(), Tier: TierCold, ColdKey: o.Key,
			Provenance: v.provenance(o.Key), Class: ClassActive, UpdatedAt: ms(now)}
		if m, ok := man[o.Key]; ok && m.Size == o.Size {
			r.MtimeNS, r.ColdSHA256 = m.MtimeNS, m.SHA256
			if m.Provenance.valid() {
				r.Provenance = m.Provenance
			}
		} else if f, ok := listed[o.Key]; ok && f.Size == o.Size {
			r.MtimeNS, r.ColdSHA256 = f.MTime.UnixNano(), f.SHA256
		}
		r.SHA256 = r.ColdSHA256
		r.LastAccess = ms(time.Unix(0, r.MtimeNS))
		rows[o.Key] = r
	}
	for rel, fi := range hot {
		sum, n, err := v.hashFile(rel)
		if err != nil {
			return rep, err
		}
		r, inCold := rows[rel]
		if !inCold {
			r = row{Path: rel, Provenance: v.provenance(rel), Class: ClassActive}
		}
		r.Size, r.MtimeNS, r.SHA256 = n, fi.ModTime().UnixNano(), sum
		r.LastAccess = ms(fi.ModTime())
		r.UpdatedAt = ms(now)
		r.Tier = TierHot
		if inCold && r.ColdSHA256 == sum {
			r.Tier = TierBoth
		} else if inCold && r.ColdSHA256 == "" {
			// Cold has something here, but nothing says what: upload the
			// hot file again rather than trust it.
			r.ColdKey = ""
		}
		rows[rel] = r
	}

	ordered := make([]row, 0, len(rows))
	for _, r := range rows {
		ordered = append(ordered, r)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })

	v.mu.Lock()
	defer v.mu.Unlock()
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return rep, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM files`); err != nil {
		return rep, errors.Join(err, tx.Rollback())
	}
	for _, r := range ordered {
		if err := putRow(ctx, tx, r); err != nil {
			return rep, errors.Join(err, tx.Rollback())
		}
		switch r.Tier {
		case TierHot:
			rep.Hot++
		case TierCold:
			rep.Cold++
			if r.ColdSHA256 == "" {
				rep.UnknownHash = append(rep.UnknownHash, r.Path)
			}
			if v.policy.matchPinned(r.Path) {
				rep.PinnedCold = append(rep.PinnedCold, r.Path)
			}
		case TierBoth:
			rep.Both++
		}
	}
	if err := tx.Commit(); err != nil {
		return rep, err
	}
	rep.Rows = len(ordered)
	return rep, v.rewriteColdListings(ordered)
}
