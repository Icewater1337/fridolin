package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/files"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/runsnap"
)

// Putting a run back (docs/BRAINS.md, "Undoing a run"): the owner's
// !undo in their direct chat, the dashboard's Audit page of a run
// (GET /api/runs/{run}/files, POST /api/runs/{run}/undo) and
// `fridolin runs`. Each first shows what it would do; a file that
// changed since the run is left as it is and listed.

// runFilesView is GET /api/runs/{run}/files: the record of what run
// changed, and what undoing it would do now.
type runFilesView struct {
	Record  *runsnap.Record     `json:"record"`
	Plan    *runsnap.UndoResult `json:"plan,omitempty"`
	Running bool                `json:"running"`
}

// runFilesListLimit bounds GET /api/run-files and !undo's list.
const (
	runFilesListLimit = 200
	undoListRuns      = 5
)

// undoError classifies an error of the store for the API.
func undoError(err error) error {
	switch {
	case errors.Is(err, runsnap.ErrBadID), errors.Is(err, fs.ErrNotExist):
		return apperr.Wrap(apperr.NotFound, fmt.Errorf("no record of that run's files: it changed none, ended longer ago than run_files.keep_days, or is no run"))
	case errors.Is(err, runsnap.ErrRunning):
		return apperr.Wrap(apperr.Conflict, err)
	case errors.Is(err, runsnap.ErrNotRecorded):
		return apperr.Wrap(apperr.Precondition, err)
	}
	return err
}

// view answers GET /api/runs/{run}/files.
func (f *runFilesService) view(_ context.Context, run string) (any, error) {
	if f.store.Running(run) {
		return runFilesView{Running: true}, nil
	}
	rec, err := f.store.Load(run)
	if err != nil {
		return nil, undoError(err)
	}
	out := runFilesView{Record: rec}
	if rec.Skipped == "" {
		plan, err := f.store.Undo(run, runsnap.UndoOptions{DryRun: true})
		if err != nil {
			return nil, undoError(err)
		}
		out.Plan = plan
	}
	return out, nil
}

// list answers GET /api/run-files: the records of brain ("" for all),
// newest first.
func (f *runFilesService) list(_ context.Context, brain string, limit int) (any, error) {
	if limit <= 0 || limit > runFilesListLimit {
		limit = runFilesListLimit
	}
	recs, err := f.store.List(brain, limit)
	if err != nil {
		return nil, err
	}
	type item struct {
		Run       string     `json:"run"`
		Brain     string     `json:"brain"`
		Chat      string     `json:"chat,omitempty"`
		Started   time.Time  `json:"started"`
		Ended     time.Time  `json:"ended"`
		Summary   string     `json:"summary"`
		Created   int        `json:"created"`
		Changed   int        `json:"changed"`
		Deleted   int        `json:"deleted"`
		Skipped   string     `json:"skipped,omitempty"`
		Alongside []string   `json:"alongside,omitempty"`
		Undone    *time.Time `json:"undone,omitempty"`
	}
	out := []item{}
	for _, r := range recs {
		it := item{Run: r.Run, Brain: r.Brain, Chat: r.Chat, Started: r.Started, Ended: r.Ended, Summary: r.Summary(runFilesAuditPaths), Skipped: r.Skipped, Alongside: r.Alongside}
		it.Created, it.Changed, it.Deleted = r.Counts()
		if r.Undone != nil {
			it.Undone = &r.Undone.At
		}
		out = append(out, it)
	}
	return out, nil
}

// undo puts run back for by, or with dry says what it would do. A real
// undo writes an audit line of kind run_files.
func (f *runFilesService) undo(run, by string, dry bool) (*runsnap.UndoResult, error) {
	res, err := f.store.Undo(run, runsnap.UndoOptions{DryRun: dry, By: by, Trash: files.TrashDir})
	if dry || res == nil {
		return res, undoError(err)
	}
	restored, trashed := res.Count(runsnap.ActRestore, true), res.Count(runsnap.ActTrash, true)
	detail := fmt.Sprintf("undo: %d written back, %d to the trash, %d left as they changed since", restored, trashed, res.Count(runsnap.ActConflict, false))
	outcome := "ok"
	if failed := res.Failed(); len(failed) > 0 {
		var paths []string
		for _, s := range failed[:min(len(failed), runFilesAuditPaths)] {
			paths = append(paths, s.Path)
		}
		outcome = fmt.Sprintf("failed for %d: %s", len(failed), strings.Join(paths, ", "))
	} else if err != nil {
		outcome = "failed: " + err.Error()
	}
	f.b.audit.Append(audit.Entry{Kind: audit.RunFiles, Brain: res.Brain, Run: run, By: by, Detail: detail, Outcome: outcome})
	log.Printf("run files: run %s of %s undone by %s: %s (%s)", run, res.Brain, by, detail, outcome)
	return res, undoError(err)
}

// undoRun answers POST /api/runs/{run}/undo.
func (f *runFilesService) undoRun(_ context.Context, run string, dry bool) (any, error) {
	return f.undo(run, "dashboard", dry)
}

// undoCommand answers the owner's !undo: alone, the runs that changed
// files; with a run, what undoing it would do; with a run and yes, the
// undo.
func (b *App) undoCommand(args []string, by string, record func(id, outcome string)) string {
	f := b.runFiles
	keep := b.cfgs.Get().RunFiles.KeepDays
	now := time.Now()
	switch {
	case len(args) == 0:
		recs, err := f.store.List("", undoListRuns)
		if err != nil {
			record("", "failed: "+err.Error())
			return outboundmsg.ChatErrGeneric
		}
		var runs []outboundmsg.UndoRun
		for _, r := range recs {
			runs = append(runs, outboundmsg.UndoRun{Run: r.Run, Brain: r.Brain, Summary: r.Summary(3), Ended: r.Ended, Undone: r.Undone != nil})
		}
		record("", "ok: listed")
		return outboundmsg.UndoListReply(runs, keep, now)
	case len(args) > 2 || len(args) == 2 && !strings.EqualFold(args[1], "yes"):
		record("", "refused: usage")
		return outboundmsg.UndoUsage
	}
	run, dry := args[0], len(args) == 1
	res, err := f.undo(run, by, dry)
	switch {
	case errors.Is(err, runsnap.ErrRunning):
		record(run, "refused: running")
		return outboundmsg.UndoRunningReply(run)
	case errors.Is(err, runsnap.ErrNotRecorded):
		record(run, "refused: not recorded")
		return outboundmsg.UndoNotRecordedReply(run, strings.TrimPrefix(err.Error(), runsnap.ErrNotRecorded.Error()+": "))
	case apperr.KindOf(err) == apperr.NotFound:
		record(run, "refused: no record")
		return outboundmsg.UndoNoRecordReply(run, keep)
	case res == nil:
		log.Printf("run files: !undo %s: %v", run, err)
		record(run, "failed")
		return outboundmsg.ChatErrGeneric
	}
	steps := make([]outboundmsg.UndoStep, len(res.Steps))
	for i, s := range res.Steps {
		steps[i] = outboundmsg.UndoStep{Path: s.Path, Op: s.Op, Action: s.Action, Why: s.Why, Done: s.Done, Error: s.Error}
	}
	if dry {
		rec, _ := f.store.Load(run)
		var ended, undoneAt time.Time
		undoneBy := ""
		if rec != nil {
			ended = rec.Ended
			if rec.Undone != nil {
				undoneAt, undoneBy = rec.Undone.At, rec.Undone.By
			}
		}
		record(run, "ok: dry run")
		return outboundmsg.UndoPlanReply(run, res.Brain, ended, now, undoneAt, undoneBy, steps)
	}
	record(run, "ok: undone")
	return outboundmsg.UndoDoneReply(run, steps)
}
