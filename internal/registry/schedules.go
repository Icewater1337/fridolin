package registry

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"slices"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// A brain's scheduled agents live in its folder, one file each
// (.brain/schedules/<name>.json), and run as the owner approved them:
// the approved copy of the brain in config.json holds them, as it holds
// the rest of brain.json. Each file is approved on its own; brain.json's
// approval leaves them aside (manifest.EqualBrain).

// ScheduleReviews are the scheduled agents of brain id whose definition
// on disk differs from the approved one: new, changed, removed or
// invalid. A folder that cannot be read has none.
func (g *Registry) ScheduleReviews(cfg *config.Settings, id string) []manifest.ScheduleReview {
	for _, ref := range cfg.BrainDirs {
		if ref.Approved == nil || ref.Approved.ID != id {
			continue
		}
		disk, err := manifest.ReadDiskSchedules(ref.Dir(g.options.Home()))
		if err != nil {
			return nil
		}
		return manifest.ReviewSchedules(disk, *ref.Approved)
	}
	return nil
}

// ErrReviewedScheduleChanged refuses an approval of an agent whose file
// is no longer the one reviewed.
var ErrReviewedScheduleChanged = apperr.New(apperr.Conflict, "the agent's file changed again since you reviewed it — look at the new version and approve that")

// ApproveSchedule approves scheduled agent name of brain id as it is on
// disk, which must be what the owner reviewed: reviewed nil approves its
// removal. The files are not touched.
func (g *Registry) ApproveSchedule(id, name string, reviewed *manifest.Schedule) error {
	_, err := g.store.UpdateTx("", func(c *config.File, tx *config.Tx) error {
		for i := range c.BrainDirs {
			ref := &c.BrainDirs[i]
			if ref.Approved == nil || ref.Approved.ID != id {
				continue
			}
			disk, err := manifest.ReadDiskSchedules(ref.Dir(g.options.Home()))
			if err != nil {
				return err
			}
			reviews := manifest.ReviewSchedules(disk, *ref.Approved)
			k := slices.IndexFunc(reviews, func(r manifest.ScheduleReview) bool { return r.Name == name })
			if k < 0 {
				return apperr.New(apperr.NotFound, fmt.Sprintf("brain %s has no change to scheduled agent %q waiting for approval", id, name))
			}
			r := reviews[k]
			m := *ref.Approved
			switch {
			case r.Status == manifest.ReviewInvalid:
				return apperr.New(apperr.Invalid, fmt.Sprintf("%s cannot be approved as it is: %s", r.Source, r.Detail))
			case r.Status == manifest.ReviewRemoved:
				if reviewed != nil {
					return ErrReviewedScheduleChanged
				}
				m.RemoveSchedule(name)
			default:
				if reviewed == nil || !r.Disk.Equal(*reviewed) {
					return ErrReviewedScheduleChanged
				}
				m.SetSchedule(*r.Disk)
			}
			if err := m.Validate(); err != nil {
				return apperr.Wrap(apperr.Invalid, err)
			}
			n := m.Normalized()
			ref.Approved = &n
			tx.CheckBrains()
			return nil
		}
		return apperr.New(apperr.NotFound, fmt.Sprintf("no brain %q", id))
	})
	return err
}

// PutSchedule writes scheduled agent s of brain id, the owner's own edit:
// its file and the approved copy at once, so it needs no approval after.
// Like every edit in the dashboard it writes brain.json as approved.
func (g *Registry) PutSchedule(id string, s manifest.Schedule) error {
	data, err := manifest.EncodeSchedule(s)
	if err != nil {
		return apperr.Wrap(apperr.Invalid, err)
	}
	_, err = g.store.UpdateTx("", func(c *config.File, tx *config.Tx) error {
		dir, ok := g.brainDir(c, id)
		if !ok {
			return apperr.New(apperr.NotFound, fmt.Sprintf("no brain %q", id))
		}
		if err := g.updateManifestIn(c, tx, id, func(m *manifest.Manifest) error {
			m.SetSchedule(s)
			if err := m.Validate(); err != nil {
				return apperr.Wrap(apperr.Invalid, err)
			}
			return nil
		}); err != nil {
			return err
		}
		return tx.WriteBrainFile(dir, manifest.ScheduleFile(s.Name), data)
	})
	return err
}

// RemoveSchedule removes scheduled agent name of brain id: its file and
// its approved copy, or the file alone of one never approved.
func (g *Registry) RemoveSchedule(id, name string) error {
	if !manifest.ValidScheduleName(name) {
		return apperr.New(apperr.NotFound, fmt.Sprintf("brain %s has no scheduled agent %q", id, name))
	}
	_, err := g.store.UpdateTx("", func(c *config.File, tx *config.Tx) error {
		dir, ok := g.brainDir(c, id)
		if !ok {
			return apperr.New(apperr.NotFound, fmt.Sprintf("no brain %q", id))
		}
		disk, err := manifest.ReadDiskSchedules(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		found := slices.ContainsFunc(disk, func(d manifest.DiskSchedule) bool { return d.Name == name })
		if err := g.updateManifestIn(c, tx, id, func(m *manifest.Manifest) error {
			if m.RemoveSchedule(name) {
				found = true
			}
			return nil
		}); err != nil {
			return err
		}
		if !found {
			return apperr.New(apperr.NotFound, fmt.Sprintf("brain %s has no scheduled agent %q", id, name))
		}
		return tx.RemoveBrainFile(dir, manifest.ScheduleFile(name))
	})
	return err
}

// brainDir is the folder of approved brain id in c.
func (g *Registry) brainDir(c *config.File, id string) (string, bool) {
	for _, ref := range c.BrainDirs {
		if ref.Approved != nil && ref.Approved.ID == id {
			return ref.Dir(g.options.Home()), true
		}
	}
	return "", false
}

// MigrateScheduleFiles moves every configured brain's brain.json
// schedules into their files (manifest.MigrateInlineSchedules), which
// the bridge does as it starts. Nothing that runs, nor anything approved,
// changes; a brain whose folder cannot be read is left as it is.
func (g *Registry) MigrateScheduleFiles() {
	for _, ref := range g.store.Get().BrainDirs {
		if ref.Approved == nil {
			continue
		}
		if _, err := manifest.MigrateInlineSchedules(ref.Dir(g.options.Home())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("brain %s: moving its scheduled agents out of brain.json: %v", ref.Approved.ID, err)
		}
	}
}
