package vault

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchOptions adjust Watch.
type WatchOptions struct {
	// Debounce is how long the hot tier must be quiet before the changed
	// paths are synced (default 2s).
	Debounce time.Duration
	// MaxDelay bounds how long a change waits while writes keep coming
	// (default 1m).
	MaxDelay time.Duration
	// OnSync, when set, gets each sync's report.
	OnSync func(SyncReport, error)
}

// Watch follows the hot tier with fsnotify until ctx ends, and feeds the
// paths that changed to SyncPaths once writes settle. It ignores what the
// vault ignores (vault.db*, bridge.db*, *.tmp, .cold.json). When the
// kernel drops events, the next round is a full Sync. Watch does no sync
// of its own at start; run Sync once first to catch up.
func (v *Vault) Watch(ctx context.Context, opts WatchOptions) error {
	if opts.Debounce <= 0 {
		opts.Debounce = 2 * time.Second
	}
	if opts.MaxDelay <= 0 {
		opts.MaxDelay = time.Minute
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()
	if err := v.watchTree(w, "."); err != nil {
		return err
	}

	pending := map[string]bool{}
	full := false
	var first time.Time
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	schedule := func() {
		now := time.Now()
		if first.IsZero() {
			first = now
		}
		wait := opts.Debounce
		if left := opts.MaxDelay - now.Sub(first); left < wait {
			wait = max(left, 0)
		}
		timer.Reset(wait)
	}
	flush := func() {
		paths := make([]string, 0, len(pending))
		for p := range pending {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		clear(pending)
		first = time.Time{}
		var rep SyncReport
		var err error
		if full {
			full = false
			rep, err = v.Sync(ctx)
		} else {
			rep, err = v.SyncPaths(ctx, paths)
		}
		if err != nil && ctx.Err() == nil {
			v.log.Warn("vault: sync after changes failed", "err", err)
		}
		if opts.OnSync != nil {
			opts.OnSync(rep, err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			rel, ok := v.relOf(ev.Name)
			if !ok || v.ignored(rel) {
				continue
			}
			if ev.Has(fsnotify.Create) {
				if fi, err := v.lstat(rel); err == nil && fi.IsDir() {
					if err := v.watchTree(w, rel); err != nil {
						v.log.Warn("vault: watching a new folder failed", "path", rel, "err", err)
						full = true
					}
				}
			}
			pending[rel] = true
			schedule()
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				full = true
				schedule()
				continue
			}
			v.log.Warn("vault: watcher error", "err", err)
		case <-timer.C:
			flush()
		}
	}
}

// relOf turns a watcher's absolute name into a vault path.
func (v *Vault) relOf(name string) (string, bool) {
	rel, err := filepath.Rel(v.opts.Root, name)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// watchTree adds rel and every folder below it, except ignored ones.
func (v *Vault) watchTree(w *fsnotify.Watcher, rel string) error {
	return fs.WalkDir(v.root.FS(), filepath.FromSlash(rel), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if p != "." && v.ignored(filepath.ToSlash(p)) {
			return fs.SkipDir
		}
		return w.Add(v.abs(filepath.ToSlash(p)))
	})
}
