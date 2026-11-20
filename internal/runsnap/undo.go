package runsnap

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// Putting a run back: each file the run changed or deleted is written back
// as it was before the run (through fsx.WriteInRoot, so whole or not at
// all), and each file it created goes into the folder's trash, never
// away. A file is touched only while it is what the run left: one that
// changed since (by the owner, or by a later run) is a conflict, left as
// it is and listed, so an undo never takes back anything but the run's
// own work. A dry run says the same without touching anything.

// The actions of an undo's steps.
const (
	// ActRestore writes the file back as it was before the run: one the
	// run changed or deleted.
	ActRestore = "restore"
	// ActTrash moves a file the run created into the folder's trash.
	ActTrash = "trash"
	// ActNone leaves a file that is as it was before the run already.
	ActNone = "none"
	// ActConflict leaves a file that changed since the run.
	ActConflict = "conflict"
)

// Step is what undoing one change does, or did.
type Step struct {
	Change
	Action string `json:"action"`
	// Why says why a step is none or a conflict.
	Why string `json:"why,omitempty"`
	// To is where a trashed file went, relative to its folder.
	To string `json:"to,omitempty"`
	// Done is a step Undo took; Error why it could not.
	Done  bool   `json:"done,omitempty"`
	Error string `json:"error,omitempty"`
}

// UndoResult is a run's undo: what it would do (a dry run) or did.
type UndoResult struct {
	Run    string  `json:"run"`
	Brain  string  `json:"brain"`
	DryRun bool    `json:"dry_run"`
	Steps  []Step  `json:"steps"`
	Undone *Undone `json:"undone,omitempty"`
}

// Count is how many steps are action; with done, only those taken.
func (u *UndoResult) Count(action string, done bool) int {
	n := 0
	for _, s := range u.Steps {
		if s.Action == action && (!done || s.Done) {
			n++
		}
	}
	return n
}

// Failed are the steps Undo could not take.
func (u *UndoResult) Failed() []Step {
	var out []Step
	for _, s := range u.Steps {
		if s.Error != "" {
			out = append(out, s)
		}
	}
	return out
}

// Errors of an undo.
var (
	// ErrRunning is a run that has not ended: its record is not there yet.
	ErrRunning = errors.New("the run has not ended yet")
	// ErrNotRecorded is a run whose files were not recorded.
	ErrNotRecorded = errors.New("the run's files were not recorded")
)

// UndoOptions say how to undo a run.
type UndoOptions struct {
	// DryRun only says what Undo would do.
	DryRun bool
	// By is who asked, for the record.
	By string
	// Trash is the folder, relative to each of the run's folders, that
	// files the run created go to, as Trash/<time>/<path>.
	Trash string
}

// Undo puts run back as opt says, or with opt.DryRun says what it would
// do. The record notes when it was undone, and by whom.
func (s *Store) Undo(run string, opt UndoOptions) (*UndoResult, error) {
	if s.Running(run) {
		return nil, ErrRunning
	}
	if !opt.DryRun {
		// Shared with the walks, and the brain's walks wait: no object the
		// undo reads goes, and no walk of the brain sees half of it.
		s.gc.RLock()
		defer s.gc.RUnlock()
	}
	rec, err := s.Load(run)
	if err != nil {
		return nil, err
	}
	if rec.Skipped != "" {
		return nil, fmt.Errorf("%w: %s", ErrNotRecorded, rec.Skipped)
	}
	if !opt.DryRun {
		lock := s.brainLock(rec.Brain)
		lock.Lock()
		defer lock.Unlock()
	}
	if !opt.DryRun && opt.Trash == "" {
		return nil, errors.New("no trash folder to put the run's new files in")
	}
	out := &UndoResult{Run: rec.Run, Brain: rec.Brain, DryRun: opt.DryRun, Steps: []Step{}, Undone: rec.Undone}
	roots := map[string]*os.Root{}
	defer func() {
		for _, r := range roots {
			if r != nil {
				r.Close()
			}
		}
	}()
	dirs := map[string]string{}
	for _, r := range rec.Roots {
		dirs[r.Name] = r.Dir
	}
	stamp := s.now().UTC().Format("20060102-150405")
	rootOf := func(name string) *os.Root {
		root, ok := roots[name]
		if !ok {
			if dir, known := dirs[name]; known {
				root, _ = os.OpenRoot(dir)
			}
			roots[name] = root
		}
		return root
	}
	renamed := s.renames(rec.Changes, rootOf)
	steps := make([]Step, len(rec.Changes))
	for i, c := range rec.Changes {
		root := rootOf(c.Root)
		if j, ok := renamed[i]; ok {
			if c.Op == OpDeleted {
				steps[i], steps[j] = s.undoRename(root, c, rec.Changes[j], opt.DryRun, stamp)
			}
			continue
		}
		step := s.plan(root, c)
		if !opt.DryRun && (step.Action == ActRestore || step.Action == ActTrash) {
			if err := s.take(root, &step, opt.Trash, stamp); err != nil {
				step.Error = err.Error()
			} else {
				step.Done = true
			}
		}
		steps[i] = step
	}
	out.Steps = append(out.Steps, steps...)
	if !opt.DryRun {
		rec.Undone = &Undone{At: s.now().UTC(), By: opt.By}
		out.Undone = rec.Undone
		if err := s.save(rec); err != nil {
			return out, fmt.Errorf("noting the undo in the record: %w", err)
		}
	}
	return out, nil
}

// renames pairs, both ways by index, each change that deleted a path with
// one that created a path that is the same file now: a rename the
// filesystem does not tell apart, by case on a case-insensitive one, or by
// Unicode normalisation on APFS. Undone apart, the old name would find the
// file "there again" and the new one would trash it.
func (s *Store) renames(changes []Change, rootOf func(string) *os.Root) map[int]int {
	out := map[int]int{}
	for i, d := range changes {
		if d.Op != OpDeleted {
			continue
		}
		root := rootOf(d.Root)
		if root == nil {
			continue
		}
		for j, c := range changes {
			if _, taken := out[j]; taken || c.Op != OpCreated || c.Root != d.Root {
				continue
			}
			if s.sameFile(root, d.Path, c.Path) {
				out[i], out[j] = j, i
				break
			}
		}
	}
	return out
}

// sameFile says whether the paths a and b of root name the same file.
func sameFile(root *os.Root, a, b string) bool {
	fa, err := root.Lstat(a)
	if err != nil {
		return false
	}
	fb, err := root.Lstat(b)
	return err == nil && os.SameFile(fa, fb)
}

// undoRename undoes a rename of the file the change del deleted to the one
// cre created: the file goes back to its old name, with its earlier
// contents if the run changed them too, while it is what the run left.
func (s *Store) undoRename(root *os.Root, del, cre Change, dryRun bool, stamp string) (Step, Step) {
	d, c := Step{Change: del}, Step{Change: cre}
	conflict := func(why string) (Step, Step) {
		d.Action, d.Why = ActConflict, why
		c.Action, c.Why = ActConflict, why
		return d, c
	}
	cur, mode, exists, err := current(root, cre.Path)
	switch {
	case err != nil:
		return conflict(err.Error())
	case !exists || cre.After == nil || cur != cre.After.Hash:
		return conflict("it changed since the run")
	}
	rewrite := cur != del.Before.Hash || mode != del.Before.Mode
	if rewrite && !s.obj.has(del.Before.Hash) {
		return conflict("its earlier contents are no longer stored")
	}
	d.Action = ActRestore
	c.Action, c.Why = ActNone, "it is renamed back to "+del.Path
	if dryRun {
		return d, c
	}
	if err := s.renameBack(root, cre.Path, del.Path, stamp); err != nil {
		d.Error, c.Error = err.Error(), err.Error()
		return d, c
	}
	c.Done = true
	if rewrite {
		if err := s.take(root, &d, "", stamp); err != nil {
			d.Error = err.Error()
			return d, c
		}
	}
	d.Done = true
	return d, c
}

// renameBack renames from to to in root through a free temporary name, as
// a rename that only changes case needs on a case-insensitive filesystem.
func (s *Store) renameBack(root *os.Root, from, to, stamp string) error {
	for n := 1; n <= 100; n++ {
		tmp := path.Join(path.Dir(to), ".fridolin-undo-"+stamp+"-"+strconv.Itoa(n))
		if _, err := root.Lstat(tmp); err == nil {
			continue
		}
		if err := root.Rename(from, tmp); err != nil {
			return err
		}
		// Once moved aside, the old name must be free: another file
		// there is not the run's to replace.
		if _, err := root.Lstat(to); err == nil {
			return errors.Join(errors.New("another file is there now"), root.Rename(tmp, from))
		}
		if err := root.Rename(tmp, to); err != nil {
			return errors.Join(err, root.Rename(tmp, from))
		}
		return nil
	}
	return errors.New("no free temporary name to rename it back through")
}

// plan decides what undoing c does to the file as it is now in root (nil
// for a folder that is gone).
func (s *Store) plan(root *os.Root, c Change) Step {
	st := Step{Change: c}
	conflict := func(why string) Step {
		st.Action, st.Why = ActConflict, why
		return st
	}
	if root == nil {
		return conflict("its folder is not there any more")
	}
	cur, mode, exists, err := current(root, c.Path)
	if err != nil {
		return conflict(err.Error())
	}
	switch c.Op {
	case OpCreated:
		switch {
		case !exists:
			st.Action, st.Why = ActNone, "it is gone already"
		case cur == c.After.Hash:
			st.Action = ActTrash
		default:
			return conflict("it changed since the run")
		}
	case OpChanged:
		switch {
		case !exists:
			return conflict("it was deleted since the run")
		case cur == c.Before.Hash && mode == c.Before.Mode:
			st.Action, st.Why = ActNone, "it is as it was before the run"
		case cur == c.After.Hash:
			st.Action = ActRestore
		default:
			return conflict("it changed since the run")
		}
	case OpDeleted:
		switch {
		case !exists:
			st.Action = ActRestore
		case cur == c.Before.Hash:
			st.Action, st.Why = ActNone, "it is there again"
		default:
			return conflict("another file is there now")
		}
	default:
		return conflict("the record does not say what the run did to it")
	}
	if st.Action == ActRestore && !s.obj.has(c.Before.Hash) {
		return conflict("its earlier contents are no longer stored")
	}
	return st
}

// current is the hash and permission bits of the file rel of root, and
// whether there is one. Something there that is not a regular file is an
// error.
func current(root *os.Root, rel string) (string, uint32, bool, error) {
	fi, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	if !fi.Mode().IsRegular() {
		return "", 0, true, errors.New("it is no longer a file")
	}
	f, err := fsx.OpenInFolder(root, rel, os.O_RDONLY, 0)
	if err != nil {
		return "", 0, true, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, true, err
	}
	return hex.EncodeToString(h.Sum(nil)), uint32(fi.Mode().Perm()), true, nil
}

// take takes step in root: writes the file back, or moves it to trash.
func (s *Store) take(root *os.Root, step *Step, trash, stamp string) error {
	if step.Action == ActRestore {
		rc, err := s.obj.open(step.Before.Hash)
		if err != nil {
			return err
		}
		defer rc.Close()
		return fsx.WriteInRoot(root, step.Path, rc, fs.FileMode(step.Before.Mode)&fs.ModePerm)
	}
	for n := 1; n <= 100; n++ {
		at := stamp
		if n > 1 {
			at += "-" + strconv.Itoa(n)
		}
		dst := path.Join(trash, at, step.Path)
		if _, err := root.Lstat(dst); err == nil {
			continue
		}
		if err := root.MkdirAll(path.Dir(dst), 0o700); err != nil {
			return err
		}
		if fi, err := root.Lstat(trash); err != nil || !fi.IsDir() {
			return errors.New("the folder's trash is not a folder")
		}
		if err := root.Rename(step.Path, dst); err != nil {
			return err
		}
		step.To = dst
		return nil
	}
	return errors.New("the trash has no free name for it")
}
