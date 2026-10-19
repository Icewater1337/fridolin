// Package site is the site process (server, web), the starter files for
// an empty site repository (template) and the builder plugin (builder).
// The binary carries them so the site always matches the Fridolin that
// runs it; SiteManager unpacks them into <data_dir>/site/shell and
// installs their dependencies. Besides the embedded files this folder is
// a Node project of its own (docs/SITE.md); it holds no Go code.
package site

import "embed"

// FS holds the files at the same paths as in this folder.
//
// builder is the plugin a builder brain's runs load (site_builder.go).
//
//go:embed server web package.json package-lock.json all:template all:builder
var FS embed.FS
