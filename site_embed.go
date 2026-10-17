package main

import "embed"

// siteShellFS is the site process (site/server, site/web) and the starter
// files for an empty site repository (site/template). The binary carries
// them so the site always matches the Fridolin that runs it; SiteManager
// unpacks them into <data_dir>/site/shell and installs their dependencies.
//
//go:embed site/server site/web site/package.json site/package-lock.json all:site/template
var siteShellFS embed.FS
