// Package container holds the fridolin-core image's build files. The
// compose file is compiled into the binary, so `fridolin core start` runs
// the one of its own version wherever it is installed.
package container

import _ "embed"

// Compose is compose.yaml, the core's service and volume.
//
//go:embed compose.yaml
var Compose []byte
