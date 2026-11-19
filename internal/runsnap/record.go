package runsnap

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// recordVersion is the version of the records this package writes.
const recordVersion = 1

// The ops of a change.
const (
	OpCreated = "created"
	OpChanged = "changed"
	OpDeleted = "deleted"
)

// Record is what one run changed in its folders.
type Record struct {
	Version int    `json:"v"`
	Run     string `json:"run"`
	Brain   string `json:"brain"`
	Chat    string `json:"chat,omitempty"`
	Thread  string `json:"thread,omitempty"`
	// Started and Ended are when Begin and End walked the folders.
	Started time.Time  `json:"started"`
	Ended   time.Time  `json:"ended"`
	Roots   []RootInfo `json:"roots"`
	// Skipped says why the run's files were not recorded; its Changes are
	// then empty.
	Skipped string `json:"skipped,omitempty"`
	// Alongside are the runs of the same brain that ran at some time
	// between Started and Ended: what they changed then is in Changes too.
	Alongside []string `json:"alongside,omitempty"`
	Changes   []Change `json:"changes"`
	// Undone is when the run was put back, and by whom.
	Undone *Undone `json:"undone,omitempty"`
}

// RootInfo is one folder of a record.
type RootInfo struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

// Change is one file a run created, changed or deleted: Before is it as
// the run started (nil for one it created), After as it left it (nil for
// one it deleted).
type Change struct {
	Root   string `json:"root"`
	Path   string `json:"path"`
	Op     string `json:"op"`
	Before *File  `json:"before,omitempty"`
	After  *File  `json:"after,omitempty"`
}

// Undone records an undo.
type Undone struct {
	At time.Time `json:"at"`
	By string    `json:"by"`
}

// Counts are a record's changes by op.
func (r *Record) Counts() (created, changed, deleted int) {
	for _, c := range r.Changes {
		switch c.Op {
		case OpCreated:
			created++
		case OpChanged:
			changed++
		case OpDeleted:
			deleted++
		}
	}
	return created, changed, deleted
}

// Summary is a record in a line: its counts and its first n paths.
func (r *Record) Summary(n int) string {
	if r.Skipped != "" {
		return "not recorded: " + r.Skipped
	}
	created, changed, deleted := r.Counts()
	var parts []string
	for _, c := range []struct {
		n    int
		what string
	}{{created, "created"}, {changed, "changed"}, {deleted, "deleted"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.what))
		}
	}
	s := strings.Join(parts, ", ")
	var paths []string
	for _, c := range r.Changes[:min(n, len(r.Changes))] {
		paths = append(paths, c.Path)
	}
	if len(paths) > 0 {
		s += ": " + strings.Join(paths, ", ")
		if len(r.Changes) > n {
			s += fmt.Sprintf(" and %d more", len(r.Changes)-n)
		}
	}
	return s
}

// diff is what changed in root from before to after, by path.
func diff(root string, before, after state) []Change {
	var out []Change
	for rel, b := range before {
		a, ok := after[rel]
		switch {
		case !ok:
			out = append(out, Change{Root: root, Path: rel, Op: OpDeleted, Before: new(b)})
		case !a.same(b):
			out = append(out, Change{Root: root, Path: rel, Op: OpChanged, Before: new(b), After: new(a)})
		}
	}
	for rel, a := range after {
		if _, ok := before[rel]; !ok {
			out = append(out, Change{Root: root, Path: rel, Op: OpCreated, After: new(a)})
		}
	}
	slices.SortFunc(out, func(x, y Change) int { return cmp.Compare(x.Path, y.Path) })
	return out
}

// recordPath is where the record of run of brain is kept.
func (s *Store) recordPath(brain, run string) string {
	return filepath.Join(s.dir, "runs", brain, run+".json")
}

// save writes rec.
func (s *Store) save(rec *Record) error {
	data, err := json.MarshalIndent(rec, "", " ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(s.recordPath(rec.Brain, rec.Run), append(data, '\n'), 0o600, false)
}

// Load reads the record of run, of whichever brain it is. A run that
// changed nothing, or whose record was pruned, has none: the error then
// wraps fs.ErrNotExist.
func (s *Store) Load(run string) (*Record, error) {
	if !idRe.MatchString(run) {
		return nil, ErrBadID
	}
	brains, err := os.ReadDir(filepath.Join(s.dir, "runs"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, b := range brains {
		if !b.IsDir() || !idRe.MatchString(b.Name()) {
			continue
		}
		rec, err := readRecord(s.recordPath(b.Name(), run))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		return rec, err
	}
	return nil, errNoRecord
}

// readRecord reads one record file.
func readRecord(p string) (*Record, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("the record %s is unreadable: %w", filepath.Base(p), err)
	}
	return &rec, nil
}

// List returns the records of brain ("" for every brain), the newest
// first, at most limit (0 for all).
func (s *Store) List(brain string, limit int) ([]*Record, error) {
	var out []*Record
	err := s.eachRecord(func(p string, _ fs.FileInfo) {
		if brain != "" && filepath.Base(filepath.Dir(p)) != brain {
			return
		}
		if rec, err := readRecord(p); err == nil {
			out = append(out, rec)
		}
	})
	slices.SortFunc(out, func(a, b *Record) int { return b.Ended.Compare(a.Ended) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, err
}

// eachRecord calls fn with the path and file info of every record.
func (s *Store) eachRecord(fn func(p string, fi fs.FileInfo)) error {
	brains, err := os.ReadDir(filepath.Join(s.dir, "runs"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, b := range brains {
		if !b.IsDir() {
			continue
		}
		dir := filepath.Join(s.dir, "runs", b.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			if fi, err := e.Info(); err == nil {
				fn(filepath.Join(dir, e.Name()), fi)
			}
		}
	}
	return nil
}
