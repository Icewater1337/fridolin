// Package web is the dashboard: the page, its styles and its ES modules,
// compiled into the binary so that a built bridge serves them without the
// checkout. It holds no Go code besides the embedded files.
package web

import "embed"

// FS is the page's files, as the dashboard serves them under /. Only the
// page is embedded: this folder also holds its JavaScript tests and the
// package.json node reads for them.
//
//go:embed index.html favicon.svg assets
var FS embed.FS
