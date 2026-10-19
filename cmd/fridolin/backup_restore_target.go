package main

import "github.com/Icewater1337/fridolin/internal/app"

import (
	"errors"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/registry"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// restoreTarget checks the folder a restore copies into, as for a new brain
// (CheckBrainPath): inside your home folder, neither the data dir nor in or
// around a brain, and not the credentials folder either; and new or empty.
// It returns the folder, resolved.
func (b *Bridge) restoreTarget(p string) (string, error) {
	cfg := b.cfgs.Get()
	home := b.cfgs.Boot().Home
	checkDir := brainDirCheck(b.cfgs)
	target, problems, _ := registry.CheckPath(resolveExisting(fsx.ExpandHome(strings.TrimSpace(p), home)), registry.PathContext{
		Home: home, Others: registryFor(b.cfgs).OtherDirs(cfg),
		CheckDir: func(dir, configured, home string) error { return checkDir(dir, configured, cfg, home) },
	})
	if creds := fsx.Canonical(cfg.Sandbox.CredentialsDir); len(problems) == 0 && creds != "" && (fsx.Within(target, creds) || fsx.Within(creds, target)) {
		problems = append(problems, "the credentials folder cannot be (or contain) the restore folder")
	}
	if len(problems) == 0 {
		if err := checkRestoreTarget(target, cfg, home); err != nil {
			problems = append(problems, "the restore folder "+err.Error()+" — pick a new folder")
		}
	}
	if len(problems) == 0 {
		if err := checkRestoreTarget(target, app.RuntimeConfig(b.cfgs), home); err != nil {
			problems = append(problems, "the restore folder "+err.Error()+" — pick a new folder")
		}
	}
	if len(problems) > 0 {
		return "", errors.New(strings.Join(problems, "; "))
	}
	// The folder comes from the request, and CheckBrainPath only reports
	// on it. Build what is listed, made and copied into from your home
	// folder and a part of it that stays inside, so that it is there by
	// construction and not only by that report.
	home = fsx.Canonical(home)
	rel, err := filepath.Rel(home, target)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", errors.New("restore into a new folder inside your home folder")
	}
	target = filepath.Join(home, rel)
	entries, err := os.ReadDir(target)
	switch {
	case err == nil && len(entries) > 0:
		return "", errors.New("the restore folder must be new or empty")
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	return target, nil
}
