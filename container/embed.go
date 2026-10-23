// Package container holds the fridolin-core image's build files. The
// compose file is compiled into the binary, so `fridolin core start` runs
// the one of its own version wherever it is installed.
package container

import _ "embed"

// Compose is compose.yaml, the core's service and volume.
//
//go:embed compose.yaml
var Compose []byte

// Seccomp is seccomp.json, the container's seccomp profile, which compose
// reads from beside the compose file: Docker's default with the namespaces
// bubblewrap makes for each brain's run allowed (docs/CORE.md).
//
//go:embed seccomp.json
var Seccomp []byte

// Box is box.yaml, the compose file of a node in a box, which
// `fridolin box compose` prints for box.sh and box.ps1.
//
//go:embed box.yaml
var Box []byte
