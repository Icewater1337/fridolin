package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// MigrationReport says what `migrate` did (or would do) per inline brain.
type MigrationReport struct {
	DryRun bool              `json:"dry_run"`
	Brains []MigrationResult `json:"brains"`
}

// MigrationResult is one brain's outcome.
type MigrationResult struct {
	ID      string `json:"id"`
	Dir     string `json:"dir"`
	Action  string `json:"action"` // written, kept (identical brain.json existed), conflict, skipped
	Detail  string `json:"detail,omitempty"`
	Warning string `json:"warning,omitempty"`
}

// MigrateInlineBrains moves every brain configured inline in config.json to
// its folder: it writes <work_dir>/brain.json and lists the folder in
// brain_dirs. Ids stay the same, so the history carries over.
//
// A folder that already has a different brain.json is reported as a conflict
// and left alone, unless force is set.
func MigrateInlineBrains(cs *ConfigStore, dryRun, force bool) (*MigrationReport, error) {
	rep := &MigrationReport{DryRun: dryRun, Brains: []MigrationResult{}}
	eff := cs.Get()
	file := cs.FileCopy()
	if len(file.Brains) == 0 {
		return rep, nil
	}

	type plan struct {
		idx      int
		manifest BrainManifest
		dir      string
	}
	var plans []plan
	for i, bc := range file.Brains {
		dir := eff.Brains[i].WorkDir
		m := ManifestFromInline(eff.Brains[i])
		res := MigrationResult{ID: m.ID, Dir: dir}
		if eff.Brains[i].PromptFile != "" && m.PromptFile == "brain-prompt.md" &&
			filepath.Clean(eff.Brains[i].PromptFile) != filepath.Join(dir, "brain-prompt.md") {
			res.Warning = "the prompt file is outside the brain folder; copy it to " + filepath.Join(dir, "brain-prompt.md")
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			res.Action, res.Detail = "skipped", "folder does not exist"
			rep.Brains = append(rep.Brains, res)
			continue
		}
		if disk, err := ReadManifest(dir); err == nil {
			if disk.Equal(m) {
				res.Action = "kept"
			} else if !force {
				res.Action, res.Detail = "conflict", "a different brain.json is already there (use --force to overwrite)"
				rep.Brains = append(rep.Brains, res)
				continue
			} else {
				res.Action = "written"
			}
		} else if errors.Is(err, os.ErrNotExist) {
			res.Action = "written"
		} else {
			res.Action, res.Detail = "conflict", err.Error()
			rep.Brains = append(rep.Brains, res)
			continue
		}
		_ = bc
		rep.Brains = append(rep.Brains, res)
		plans = append(plans, plan{idx: i, manifest: m, dir: dir})
	}

	if dryRun || len(plans) == 0 {
		return rep, nil
	}
	for _, p := range plans {
		if err := WriteManifest(p.dir, &p.manifest, true); err != nil {
			return rep, fmt.Errorf("writing %s: %w", filepath.Join(p.dir, ManifestFile), err)
		}
	}
	_, err := cs.Update("", func(c *Config) error {
		moved := map[int]bool{}
		for _, p := range plans {
			m := p.manifest
			c.BrainDirs = append(c.BrainDirs, BrainRef{Path: c.Brains[p.idx].WorkDir, Approved: &m})
			moved[p.idx] = true
		}
		var keep []BrainConfig
		for i, bc := range c.Brains {
			if !moved[i] {
				keep = append(keep, bc)
			}
		}
		c.Brains = keep
		return nil
	})
	return rep, err
}
