// Package version names the build: the version it was stamped with, or
// the git commit it was built from. It knows nothing of the bridge, and
// reads nothing but what the build put into the binary.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// version is set at build time with
// -ldflags "-X fridolin/internal/version.version=v1.2.3": make build and
// setup.sh stamp `git describe --tags --always --dirty`, release builds
// the tag (scripts/build-release.sh).
var version = "dev"

// String returns the release version, or the git commit for a local build.
func String() string {
	if version != "dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	rev, modified := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if rev == "" {
		return version
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if modified {
		rev += "+dirty"
	}
	return "dev-" + rev
}

// Line names the build, the Go release and the platform, so a log
// excerpt in a bug report says which binary wrote it.
func Line() string {
	return fmt.Sprintf("fridolin %s (%s, %s/%s)", String(), runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
