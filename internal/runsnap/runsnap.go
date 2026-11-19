// Package runsnap keeps, for each brain run, a record of the files it
// changed in the folders it may write, with their earlier contents, so
// that the owner can see what a run did and put it back (docs/BRAINS.md,
// "What a run changed").
//
// As a run starts, Begin walks its folders and takes the size,
// modification time, mode and SHA-256 of every file; as it ends, End
// walks them again and keeps what was created, changed or deleted in a
// small record, runs/<brain>/<run>.json. The bytes of every file a walk
// saw are in one content store, objects/, shared by every run of every
// brain: a file is stored once however many runs saw it, and a walk
// reads again only the files whose size or time changed since the last
// walk of the folder, which the index (index/<brain>.json) remembers.
//
// What a run's folders hold beyond Limits (files or bytes) is not
// walked: the run keeps a record that says it was skipped. The bridge's
// own files in a folder, what the brain's backup.exclude leaves out and
// what its .gitignore files ignore stay out (Root.Skip, ignore.go).
//
// Runs of one brain may overlap (its chat and background lanes): each
// record holds what changed in the folders while its run ran, whoever
// changed it, and names the runs that ran alongside.
package runsnap

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// Root is one folder a run may write.
type Root struct {
	// Name names it in a record: "brain" for the brain's folder.
	Name string
	// Dir is the folder, absolute.
	Dir string
	// Skip reports whether rel, a slash-separated path in Dir (a folder
	// when dir), stays out of the records: the bridge's own files, the
	// brain's backup.exclude. Nil skips nothing.
	Skip func(rel string, dir bool) bool
}

// RunInfo names a run for its record.
type RunInfo struct {
	ID, Brain, Chat, Thread string
	Started                 time.Time
}

// Store is the records and the content store in one folder of the data
// dir.
type Store struct {
	dir string
	obj objects
	now func() time.Time

	// gc is held shared by every walk and exclusively by Prune, which
	// must not take an object a walk just stored.
	gc sync.RWMutex
	// brains serialises the walks of each brain, which share its index.
	mu      sync.Mutex
	brains  map[string]*sync.Mutex
	running map[string]*Run
	index   map[string]map[string]state
}

// Open returns the store in dir, which it creates as it first writes.
func Open(dir string) *Store {
	return &Store{dir: dir, obj: objects{dir: filepath.Join(dir, "objects")}, now: time.Now,
		brains: map[string]*sync.Mutex{}, running: map[string]*Run{}, index: map[string]map[string]state{}}
}

// Dir is the store's folder.
func (s *Store) Dir() string { return s.dir }

// SetClock replaces the store's clock, for tests.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Run is a run whose folders Begin walked.
type Run struct {
	s     *Store
	info  RunInfo
	roots []Root
	lim   Limits
	// ignore are each root's .gitignore rules as the run started.
	ignore map[string]ignoreRules
	// base is each root's files as the run started.
	base    map[string]state
	skipped string
	// alongside are the runs of the brain that ran at the same time.
	alongside map[string]bool
}

// idRe is what a brain's and a run's ids may be, as they name files.
var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ErrBadID is a brain or run id that cannot name a record.
var ErrBadID = errors.New("not a run id")

// Begin walks roots as the run info starts: the record End keeps holds
// what changed in them from now on. A walk that fails or goes over lim
// leaves the run without a record of its files, which End says.
func (s *Store) Begin(info RunInfo, roots []Root, lim Limits) *Run {
	r := &Run{s: s, info: info, roots: roots, lim: lim, ignore: map[string]ignoreRules{}, base: map[string]state{}, alongside: map[string]bool{}}
	if info.Started.IsZero() {
		r.info.Started = s.now()
	}
	if !idRe.MatchString(info.ID) || !idRe.MatchString(info.Brain) {
		r.skipped = "the run has no id the store can keep"
		return r
	}
	s.gc.RLock()
	defer s.gc.RUnlock()
	lock := s.brainLock(info.Brain)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	for _, other := range s.running {
		if other.info.Brain == info.Brain {
			other.alongside[info.ID] = true
			r.alongside[other.info.ID] = true
		}
	}
	s.running[info.ID] = r
	s.mu.Unlock()

	c := &counter{lim: lim}
	for _, root := range roots {
		ig := ignoreRules{}
		cur, err := s.walk(info.Brain, root, ig, true, c)
		if err != nil {
			r.skipped = skipReason(err)
			r.base = nil
			return r
		}
		r.ignore[root.Name] = ig
		r.base[root.Name] = cur
	}
	return r
}

// skipReason is what a record says of a walk that failed.
func skipReason(err error) string {
	if tl, ok := errors.AsType[*TooLargeError](err); ok {
		return tl.Reason
	}
	return "its folders could not be read: " + err.Error()
}

// walk takes root's files with their hashes, storing what is new, and
// remembers them in the brain's index. The caller holds the brain's lock.
func (s *Store) walk(brain string, root Root, ig ignoreRules, learn bool, c *counter) (state, error) {
	dir, err := os.OpenRoot(root.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return state{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	cur, err := scan(dir, root.Skip, ig, learn, c)
	if err != nil {
		return nil, err
	}
	idx := s.loadIndex(brain)
	if err := hashAll(dir, cur, idx[root.Dir], s.obj); err != nil {
		return nil, err
	}
	idx[root.Dir] = cur
	s.saveIndex(brain, idx)
	return cur, nil
}

// brainLock is the lock of brain's walks.
func (s *Store) brainLock(brain string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.brains[brain]
	if l == nil {
		l = &sync.Mutex{}
		s.brains[brain] = l
	}
	return l
}

// End walks the run's folders again and keeps the record of what changed
// since Begin, when anything did or the run was skipped. It returns the
// record, or nil when nothing changed.
func (r *Run) End() (*Record, error) {
	s := r.s
	// Shared until the record is saved: until then only the run itself
	// holds the earlier contents, which Prune must keep.
	s.gc.RLock()
	defer s.gc.RUnlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, r.info.ID)
		s.mu.Unlock()
	}()
	s.mu.Lock()
	alongside := make([]string, 0, len(r.alongside))
	for id := range r.alongside {
		alongside = append(alongside, id)
	}
	s.mu.Unlock()
	slices.Sort(alongside)
	rec := &Record{Version: recordVersion, Run: r.info.ID, Brain: r.info.Brain, Chat: r.info.Chat, Thread: r.info.Thread,
		Started: r.info.Started.UTC(), Ended: s.now().UTC(), Alongside: alongside, Changes: []Change{}}
	for _, root := range r.roots {
		rec.Roots = append(rec.Roots, RootInfo{Name: root.Name, Dir: root.Dir})
	}
	if r.skipped == "" {
		lock := s.brainLock(r.info.Brain)
		lock.Lock()
		c := &counter{lim: r.lim}
		for _, root := range r.roots {
			cur, err := s.walk(r.info.Brain, root, r.ignore[root.Name], false, c)
			if err != nil {
				r.skipped = skipReason(err)
				break
			}
			rec.Changes = append(rec.Changes, diff(root.Name, r.base[root.Name], cur)...)
		}
		lock.Unlock()
	}
	if r.skipped != "" {
		rec.Skipped, rec.Changes = r.skipped, []Change{}
	} else if len(rec.Changes) == 0 {
		return nil, nil
	}
	if !idRe.MatchString(rec.Run) || !idRe.MatchString(rec.Brain) {
		return rec, nil
	}
	return rec, s.save(rec)
}

// Running reports whether the run id is between Begin and End.
func (s *Store) Running(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.running[id]
	return ok
}

// indexPath is where brain's index is kept.
func (s *Store) indexPath(brain string) string {
	return filepath.Join(s.dir, "index", brain+".json")
}

// loadIndex is brain's index: each root's files as the last walk saw
// them, by the root's folder. The caller holds the brain's lock.
func (s *Store) loadIndex(brain string) map[string]state {
	s.mu.Lock()
	idx, ok := s.index[brain]
	s.mu.Unlock()
	if ok {
		return idx
	}
	idx = map[string]state{}
	if data, err := os.ReadFile(s.indexPath(brain)); err == nil {
		if err := json.Unmarshal(data, &idx); err != nil {
			log.Printf("run files: brain %s's index is unreadable, its folders are read anew: %v", brain, err)
			idx = map[string]state{}
		}
	}
	s.mu.Lock()
	s.index[brain] = idx
	s.mu.Unlock()
	return idx
}

// saveIndex writes brain's index. One that cannot be written is logged:
// the next walk reads the files again.
func (s *Store) saveIndex(brain string, idx map[string]state) {
	data, err := json.Marshal(idx)
	if err == nil {
		err = fsx.WriteFileAtomic(s.indexPath(brain), data, 0o600, false)
	}
	if err != nil {
		log.Printf("run files: saving brain %s's index: %v", brain, err)
	}
}

// referenced adds to keep every hash the indexes and the runs in flight
// hold. The caller holds gc exclusively.
func (s *Store) referenced(keep map[string]bool) error {
	entries, err := os.ReadDir(filepath.Join(s.dir, "index"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		var idx map[string]state
		data, err := os.ReadFile(filepath.Join(s.dir, "index", name))
		if err != nil || json.Unmarshal(data, &idx) != nil {
			// An index that cannot be read keeps nothing: the next walk of
			// its brain reads its folders anew.
			continue
		}
		for _, st := range idx {
			for _, f := range st {
				keep[f.Hash] = true
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.running {
		for _, st := range r.base {
			for _, f := range st {
				keep[f.Hash] = true
			}
		}
	}
	return nil
}

// errNoRecord is a run without a record.
var errNoRecord = fmt.Errorf("no record of that run's files: %w", fs.ErrNotExist)
