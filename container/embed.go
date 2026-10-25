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

// BoxCore is box-core.yaml, the core's settings of a box (its data and
// home volumes, the published ports, the sandbox's security profile),
// which `fridolin box core-compose` prints for box.sh, box.ps1 and the
// updaters to write as core.yaml beside the box's compose file.
//
//go:embed box-core.yaml
var BoxCore []byte

// BoxUpdaterSh and BoxUpdaterPs1 are the box's updater for macOS and
// Linux, and for Windows, which `fridolin box updater` prints.
//
//go:embed box-update.sh
var BoxUpdaterSh []byte

//go:embed box-update.ps1
var BoxUpdaterPs1 []byte
