package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"
)

// hydrateTemp marks a download in progress.
const hydrateTemp = "hydrate"

// ErrInsufficientSpace is matched (errors.Is) by an *InsufficientSpaceError.
var ErrInsufficientSpace = errors.New("vault: not enough hot space")

// InsufficientSpaceError is Get refusing a hydration that would take hot
// usage above the budget less its headroom, or leave the disk with less
// than the headroom free. The caller asks the person first and then calls
// Get again with AllowOverBudget.
type InsufficientSpaceError struct {
	Path      string
	Size      int64 // bytes the file needs
	HotBytes  int64 // hot usage now
	Target    int64 // budget less headroom (0: no budget)
	FreeBytes int64 // free disk space, -1 when not known
	Headroom  int64 // bytes of headroom to keep free
}

func (e *InsufficientSpaceError) Error() string {
	return fmt.Sprintf("vault: hydrating %s needs %d bytes: hot usage %d of target %d, %d free on disk, %d headroom",
		e.Path, e.Size, e.HotBytes, e.Target, e.FreeBytes, e.Headroom)
}

func (e *InsufficientSpaceError) Is(target error) bool { return target == ErrInsufficientSpace }

// ErrHashMismatch is matched by a *HashMismatchError.
var ErrHashMismatch = errors.New("vault: hash mismatch")

// HashMismatchError: the bytes from the cold tier are not the bytes that
// were uploaded. Nothing on disk was changed.
type HashMismatchError struct {
	Path, Want, Got string
}

func (e *HashMismatchError) Error() string {
	return fmt.Sprintf("vault: %s: cold copy has sha256 %s, want %s", e.Path, e.Got, e.Want)
}

func (e *HashMismatchError) Is(target error) bool { return target == ErrHashMismatch }

// GetOptions adjust Get.
type GetOptions struct {
	// AllowOverBudget hydrates even when the budget or the disk's headroom
	// would be crossed: the person said yes.
	AllowOverBudget bool
}

// Get returns the absolute path of rel on disk, bringing it back from the
// cold tier first when it is cold: it downloads to a temp file beside it,
// checks the SHA-256 against the hash recorded at upload, gives it its
// modification time and renames it into place, then records tier both,
// the access and the hydration (which keeps it hot for the policy's hold)
// and drops it from .cold.json.
func (v *Vault) Get(ctx context.Context, rel string, opts GetOptions) (string, error) {
	rel, err := cleanStrict(rel)
	if err != nil {
		return "", err
	}
	for {
		v.mu.Lock()
		r, known, err := getRow(ctx, v.db, rel)
		if err != nil {
			v.mu.Unlock()
			return "", err
		}
		_, statErr := v.lstat(rel)
		onDisk := statErr == nil
		if onDisk {
			// On disk is on disk; a file that took the place of a cold one
			// is what Sync uploads next.
			if known && r.Tier != TierCold {
				now := ms(v.now())
				_, err = v.db.ExecContext(ctx, `UPDATE files SET last_access = MAX(last_access, ?), updated_at = ? WHERE path = ?`, now, now, rel)
			}
			v.mu.Unlock()
			return v.abs(rel), err
		}
		if !known || r.ColdKey == "" {
			v.mu.Unlock()
			return "", notFound(rel)
		}
		if wait, busy := v.hydrating[rel]; busy {
			v.mu.Unlock()
			select {
			case <-wait:
				continue // the other Get finished; look again
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		done := make(chan struct{})
		v.hydrating[rel] = done
		v.mu.Unlock()
		p, err := v.hydrate(ctx, r, opts)
		v.mu.Lock()
		delete(v.hydrating, rel)
		close(done)
		v.mu.Unlock()
		return p, err
	}
}

// checkSpace refuses a hydration of size bytes that would cross the budget
// less its headroom, or leave the disk with less than the headroom free.
func (v *Vault) checkSpace(ctx context.Context, rel string, size int64) error {
	target := v.targetBytes()
	hot, err := v.HotBytes(ctx)
	if err != nil {
		return err
	}
	headroom := v.opts.BudgetBytes * int64(v.opts.HeadroomPercent) / 100
	free := int64(-1)
	if v.opts.FreeSpace != nil {
		if free, err = v.opts.FreeSpace(v.opts.Root); err != nil {
			return fmt.Errorf("vault: free space: %w", err)
		}
	}
	if (target > 0 && hot+size > target) || (free >= 0 && free-size < headroom) {
		return &InsufficientSpaceError{Path: rel, Size: size, HotBytes: hot, Target: target, FreeBytes: free, Headroom: headroom}
	}
	return nil
}

func (v *Vault) hydrate(ctx context.Context, r row, opts GetOptions) (string, error) {
	if !opts.AllowOverBudget {
		if err := v.checkSpace(ctx, r.Path, r.Size); err != nil {
			return "", err
		}
	}
	if dir := path.Dir(r.Path); dir != "." {
		if err := v.root.MkdirAll(filepath.FromSlash(dir), 0o700); err != nil {
			return "", err
		}
	}
	tmp := tempName(r.Path, hydrateTemp)
	sum, n, err := v.download(ctx, r.ColdKey, tmp)
	if err != nil {
		_ = v.root.Remove(filepath.FromSlash(tmp))
		return "", err
	}
	if r.ColdSHA256 != "" && sum != r.ColdSHA256 {
		_ = v.root.Remove(filepath.FromSlash(tmp))
		return "", &HashMismatchError{Path: r.Path, Want: r.ColdSHA256, Got: sum}
	}
	mtime := time.Unix(0, r.MtimeNS)
	if err := v.root.Chtimes(filepath.FromSlash(tmp), mtime, mtime); err != nil {
		_ = v.root.Remove(filepath.FromSlash(tmp))
		return "", err
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	cur, ok, err := getRow(ctx, v.db, r.Path)
	if err == nil && (!ok || cur.Tier != TierCold || cur.ColdSHA256 != r.ColdSHA256) {
		err = errors.New("vault: the file changed while it was hydrating")
	}
	if err == nil {
		if _, serr := v.lstat(r.Path); serr == nil {
			// A new file took the path meanwhile: it wins, and Sync
			// uploads it; the download is not needed.
			_ = v.root.Remove(filepath.FromSlash(tmp))
			return v.abs(r.Path), nil
		}
	}
	if err != nil {
		_ = v.root.Remove(filepath.FromSlash(tmp))
		return "", err
	}
	if err := v.root.Rename(filepath.FromSlash(tmp), filepath.FromSlash(r.Path)); err != nil {
		_ = v.root.Remove(filepath.FromSlash(tmp))
		return "", err
	}
	now := ms(v.now())
	cur.Tier = TierBoth
	cur.SHA256 = sum
	cur.ColdSHA256 = sum
	cur.Size = n
	cur.LastAccess = max(cur.LastAccess, now)
	cur.HydratedAt = now
	cur.UpdatedAt = now
	if err := putRow(ctx, v.db, cur); err != nil {
		return "", err
	}
	if err := v.removeColdListing(r.Path); err != nil {
		v.log.Warn("vault: updating .cold.json after hydration failed", "path", r.Path, "err", err)
	}
	return v.abs(r.Path), nil
}

// download copies the cold object key to the hot temp file tmp and
// returns its SHA-256 and size.
func (v *Vault) download(ctx context.Context, key, tmp string) (string, int64, error) {
	rc, err := v.opts.Store.Get(ctx, key)
	if err != nil {
		return "", 0, fmt.Errorf("vault: fetching %s: %w", key, err)
	}
	defer func() { _ = rc.Close() }()
	f, err := v.root.OpenFile(filepath.FromSlash(tmp), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), rc)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, fmt.Errorf("vault: fetching %s: %w", key, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// HydratePinned brings back every pinned file that is only cold — what a
// fresh core does after Rebuild. It ignores the budget: pinned files are
// hot by definition.
func (v *Vault) HydratePinned(ctx context.Context) ([]string, error) {
	if err := v.Reclassify(ctx); err != nil {
		return nil, err
	}
	rows, err := queryRows(ctx, v.db, `WHERE pinned = 1 AND tier = 'cold' ORDER BY path`)
	if err != nil {
		return nil, err
	}
	var done []string
	var errs []error
	for _, r := range rows {
		if _, err := v.Get(ctx, r.Path, GetOptions{AllowOverBudget: true}); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Path, err))
			continue
		}
		done = append(done, r.Path)
	}
	return done, errors.Join(errs...)
}
