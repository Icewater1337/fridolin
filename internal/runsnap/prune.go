package runsnap

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Retention bounds what the store keeps.
type Retention struct {
	// KeepDays drops the records of runs that ended longer ago; 0 keeps
	// them by age.
	KeepDays int
	// MaxBytes drops the oldest records until the stored copies take no
	// more; 0 sets no bound. The copies of what the folders hold now are
	// kept whatever it says: the next walk would store them again.
	MaxBytes int64
	// Brains are the brains there are: the indexes of others go. Nil
	// keeps every index.
	Brains []string
}

// Pruned says what Prune removed, and what the store holds after it.
type Pruned struct {
	Records, Objects int
	Freed, Bytes     int64
}

// Prune drops the records r does not keep, then every stored copy that
// no record, index or run in flight needs.
func (s *Store) Prune(r Retention) (Pruned, error) {
	s.gc.Lock()
	defer s.gc.Unlock()
	var out Pruned
	// What a put cut short left behind; no put runs now.
	_ = os.RemoveAll(filepath.Join(s.obj.dir, "tmp"))
	if r.Brains != nil {
		s.dropIndexes(r.Brains)
	}

	type kept struct {
		path  string
		ended time.Time
		refs  []string
	}
	var recs []kept
	cut := time.Time{}
	if r.KeepDays > 0 {
		cut = s.now().Add(-time.Duration(r.KeepDays) * 24 * time.Hour)
	}
	err := s.eachRecord(func(p string, fi fs.FileInfo) {
		rec, err := readRecord(p)
		if err != nil {
			// Unreadable: it can undo nothing.
			if os.Remove(p) == nil {
				out.Records++
			}
			return
		}
		ended := rec.Ended
		if ended.IsZero() {
			ended = fi.ModTime()
		}
		if !cut.IsZero() && ended.Before(cut) {
			if os.Remove(p) == nil {
				out.Records++
			}
			return
		}
		k := kept{path: p, ended: ended}
		for _, c := range rec.Changes {
			for _, f := range []*File{c.Before, c.After} {
				if f != nil {
					k.refs = append(k.refs, f.Hash)
				}
			}
		}
		recs = append(recs, k)
	})
	if err != nil {
		return out, err
	}

	pinned := map[string]bool{}
	if err := s.referenced(pinned); err != nil {
		return out, err
	}
	count := map[string]int{}
	for _, k := range recs {
		for _, h := range k.refs {
			count[h]++
		}
	}
	sizes := map[string]int64{}
	paths := map[string]string{}
	err = s.obj.walk(func(sum string, size int64, p string) {
		if pinned[sum] || count[sum] > 0 {
			sizes[sum], paths[sum] = size, p
			out.Bytes += size
			return
		}
		if os.Remove(p) == nil {
			out.Objects++
			out.Freed += size
		}
	})
	if err != nil {
		return out, err
	}

	if r.MaxBytes > 0 && out.Bytes > r.MaxBytes {
		slices.SortFunc(recs, func(a, b kept) int { return a.ended.Compare(b.ended) })
		for _, k := range recs {
			if out.Bytes <= r.MaxBytes {
				break
			}
			if err := os.Remove(k.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return out, err
			}
			out.Records++
			for _, h := range k.refs {
				count[h]--
				if count[h] > 0 || pinned[h] {
					continue
				}
				if p, ok := paths[h]; ok && os.Remove(p) == nil {
					out.Objects++
					out.Freed += sizes[h]
					out.Bytes -= sizes[h]
					delete(paths, h)
				}
			}
		}
	}
	s.removeEmptyRunFolders()
	return out, nil
}

// dropIndexes removes the indexes of brains not in keep. The caller
// holds gc exclusively.
func (s *Store) dropIndexes(keep []string) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "index"))
	if err != nil {
		return
	}
	for _, e := range entries {
		brain, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || slices.Contains(keep, brain) {
			continue
		}
		_ = os.Remove(filepath.Join(s.dir, "index", e.Name()))
		s.mu.Lock()
		delete(s.index, brain)
		s.mu.Unlock()
	}
}

// removeEmptyRunFolders removes the folders of brains without records.
func (s *Store) removeEmptyRunFolders() {
	brains, err := os.ReadDir(filepath.Join(s.dir, "runs"))
	if err != nil {
		return
	}
	for _, b := range brains {
		if b.IsDir() {
			// Remove refuses a folder that is not empty.
			_ = os.Remove(filepath.Join(s.dir, "runs", b.Name()))
		}
	}
}
