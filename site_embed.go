package main

import "embed"

// siteShellFS is the site process (site/server, site/web), the starter
// files for an empty site repository (site/template) and the builder plugin. The binary carries
// them so the site always matches the Fridolin that runs it; SiteManager
// unpacks them into <data_dir>/site/shell and installs their dependencies.
//
// site/builder is the plugin a builder brain's runs load (site_builder.go).
//
//go:embed site/server site/web site/package.json site/package-lock.json all:site/template all:site/builder
var siteShellFS embed.FS
