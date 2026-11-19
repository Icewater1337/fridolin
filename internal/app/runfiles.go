package app

import (
	"log"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/backup"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/files"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/runsnap"
)

// The record each brain run keeps of the files it changed in its folder
// (internal/runsnap, docs/BRAINS.md "What a run changed"): admitRun walks
// the folder as the run starts, runEnded walks it again and keeps what
// changed in run-files/ of the data dir, with an audit line of kind
// run_files.

// runFilesAuditPaths is how many paths a run_files audit line names.
const runFilesAuditPaths = 5

// runFilesPruneEvery is how often a run's end also prunes the records,
// which the housekeeping job does too whenever it runs.
const runFilesPruneEvery = 6 * time.Hour

// runFilesService keeps the records.
type runFilesService struct {
	b     *App
	store *runsnap.Store

	mu         sync.Mutex
	lastPruned time.Time
}

func newRunFiles(b *App, layout config.Layout) *runFilesService {
	return &runFilesService{b: b, store: runsnap.Open(layout.RunFiles())}
}

// begin walks the folder of the run spec, which p admitted, unless
// run_files is off. The run's end keeps what changed (end).
func (f *runFilesService) begin(p *runPolicy, spec brainruntime.RunSpec) *runsnap.Run {
	if f == nil || p == nil || spec.Dir == "" {
		return nil
	}
	cfg := f.b.cfgs.Get()
	rf := cfg.RunFiles
	if !rf.Enabled {
		return nil
	}
	var exclude []string
	if spec.Settings != nil {
		// The manifest the bridge runs the brain with, which the brain
		// cannot change without the owner: brain.json on disk could leave
		// out what a later run then changes unseen.
		exclude = spec.Settings.Manifest.Backup.Exclude
	}
	deny := filesDeny(f.b.env, cfg.Home, cfg.DataDir, cfg.DBPath, cfg.Sandbox.DenyPaths, sandboxPolicyOf(cfg).protected(cfg.Home, ownLayout()))
	root := runsnap.Root{Name: runFilesBrainRoot, Dir: spec.Dir, Skip: runFilesSkip(spec.Dir, exclude, deny)}
	lim := runsnap.Limits{MaxFiles: rf.MaxFiles, MaxBytes: int64(rf.MaxMB) << 20}
	return f.store.Begin(runsnap.RunInfo{ID: p.ID, Brain: p.Brain, Chat: p.Chat, Thread: p.Thread, Started: p.started}, []runsnap.Root{root}, lim)
}

// runFilesBrainRoot names the brain's folder in a record.
const runFilesBrainRoot = "brain"

// runFilesBridgeFiles are the files of a brain folder the bridge writes
// itself, never a run: its settings (which wait for the owner's approval
// on the Brains page), its history and its run logs. A record leaves them
// out, and an undo never touches them.
var runFilesBridgeFiles = []string{manifestFileName, history.FileName, brainruntime.StreamLogName, brainruntime.StreamLogOld}

// manifestFileName is a brain's settings file.
const manifestFileName = "brain.json"

// coldListingName is the vault's list of a folder's cold files
// (internal/vault), which it rewrites as it evicts.
const coldListingName = ".cold.json"

// runFilesSkip is what a record of the brain folder dir leaves out: the
// bridge's own files, git's folders, the Files page's trash and the
// vault's lists, what the brain's backup.exclude and the secret files a
// backup never takes, and the paths in deny (absolute; the data dir and
// sandbox.deny_paths) that lie inside the folder.
func runFilesSkip(dir string, exclude, deny []string) func(rel string, isDir bool) bool {
	patterns := backup.ExcludePatterns(exclude, false)
	canon := fsx.Canonical(dir)
	var inside []string
	for _, d := range deny {
		d = fsx.Canonical(d)
		if d == canon || !fsx.Within(d, canon) {
			continue
		}
		if rel, err := filepath.Rel(canon, d); err == nil {
			inside = append(inside, filepath.ToSlash(rel))
		}
	}
	return func(rel string, isDir bool) bool {
		base := rel[strings.LastIndexByte(rel, '/')+1:]
		switch {
		case isDir && base == ".git",
			rel == files.TrashDir,
			!isDir && base == coldListingName,
			!isDir && slices.Contains(runFilesBridgeFiles, rel):
			return true
		}
		for _, p := range inside {
			if rel == p || strings.HasPrefix(rel, p+"/") {
				return true
			}
		}
		return backup.Excluded(rel, patterns)
	}
}

// end keeps the record of what run changed, and writes its audit line
// when anything did or it was not recorded.
func (f *runFilesService) end(p *runPolicy, run *runsnap.Run) {
	if f == nil || run == nil {
		return
	}
	rec, err := run.End()
	if err != nil {
		log.Printf("brain %s: keeping the record of run %s's files: %v", p.Brain, p.ID, err)
	}
	if rec != nil {
		outcome := "ok"
		if rec.Skipped != "" {
			outcome = "skipped: " + rec.Skipped
		} else if err != nil {
			outcome = "failed: " + err.Error()
		}
		f.b.audit.Append(audit.Entry{Kind: audit.RunFiles, Thread: p.Thread, Chat: p.Chat, Brain: p.Brain, Run: p.ID,
			Detail: rec.Summary(runFilesAuditPaths), Outcome: outcome})
	}
	f.maybePrune()
}

// maybePrune prunes the records in the background when it has not for
// runFilesPruneEvery.
func (f *runFilesService) maybePrune() {
	f.mu.Lock()
	due := time.Since(f.lastPruned) >= runFilesPruneEvery
	if due {
		f.lastPruned = time.Now()
	}
	f.mu.Unlock()
	if due {
		go func() {
			if _, err := f.prune(); err != nil {
				log.Printf("run files: pruning the records: %v", err)
			}
		}()
	}
}

// prune drops the records run_files keeps no longer, and the copies no
// record needs.
func (f *runFilesService) prune() (runsnap.Pruned, error) {
	if f == nil {
		return runsnap.Pruned{}, nil
	}
	rf := f.b.cfgs.Get().RunFiles
	var brains []string
	if f.b.brainMgr != nil {
		for _, br := range f.b.brainMgr.Brains() {
			brains = append(brains, br.ID())
		}
	}
	r := runsnap.Retention{KeepDays: rf.KeepDays, MaxBytes: int64(rf.MaxStoreMB) << 20, Brains: brains}
	if !rf.Enabled {
		// Off: what was kept goes, but for the records of the last days.
		r.Brains = []string{}
	}
	got, err := f.store.Prune(r)
	if got.Records > 0 || got.Objects > 0 {
		log.Printf("run files: dropped %d records and %d stored copies (%d MB); %d MB stay", got.Records, got.Objects, got.Freed>>20, got.Bytes>>20)
	}
	f.mu.Lock()
	f.lastPruned = time.Now()
	f.mu.Unlock()
	return got, err
}
