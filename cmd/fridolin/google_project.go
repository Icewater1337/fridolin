package main

import (
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/google"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// googleOptionsFor fixes the client path and callback address at startup;
// credentials and configured-brain checks keep their original live read sites.
func googleOptionsFor(cfgs *config.Store) google.Options {
	boot := cfgs.Boot()
	return google.Options{
		ClientPath:  boot.GoogleClientPath(),
		RedirectURI: "http://" + boot.HTTPListen + "/",
		CredsDir:    func() string { return credentialsDirOf(runtimeConfig(cfgs)) },
		Configured: func(brainID string) bool {
			for _, ref := range cfgs.Get().BrainDirs {
				if ref.Approved != nil && ref.Approved.ID == brainID {
					return true
				}
			}
			return false
		},
		UpdateManifest: func(brainID string, update func(*manifest.Manifest) error) error {
			return updateManifest(cfgs, brainID, update)
		},
	}
}
