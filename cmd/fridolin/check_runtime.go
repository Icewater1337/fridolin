package main

import (
	"log"
	"os"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/registry"
	"github.com/Icewater1337/fridolin/internal/route"
)

// checkRoutes describes the same runnable and held set as startup without
// creating runtimes. Roots are opened only to check their availability and
// closed immediately; the diagnostic command does not keep them alive.
func checkRoutes(specs []manifest.Spec, issues []registry.Issue) (*route.Table, map[string][]string, error) {
	if err := route.Validate(specs); err != nil {
		return nil, nil, err
	}
	held := heldBrainRoutes(issues)
	active := make([]manifest.Spec, 0, len(specs))
	workDirs := make(map[string][]string, len(specs))
	for _, sp := range specs {
		id := sp.Manifest.ID
		// Even a folder that cannot be opened remains denied to other brains.
		workDirs[id] = []string{sp.Dir}
		if sp.Path != "" && sp.Path != sp.Dir {
			workDirs[id] = []string{sp.Path, sp.Dir}
		}
		root, err := os.OpenRoot(sp.Dir)
		if err != nil {
			log.Printf("brain %s: leaving it out: %v", id, err)
			held = append(held, route.Held{ID: id, Routing: sp.Manifest.Routing})
			continue
		}
		root.Close()
		active = append(active, sp)
	}
	table, err := route.BuildWithHeld(active, held)
	return table, workDirs, err
}
