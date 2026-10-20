package platform

import (
	"go/build"
	"slices"
	"testing"
)

// The package builds on macOS, Linux and Windows, each with its own lock
// and process groups, and nowhere else (plan9 has neither file).
func TestTheSupportedSystemsBuildThePackage(t *testing.T) {
	for _, c := range []struct{ goos, lock string }{
		{"darwin", "lock_unix.go"}, {"linux", "lock_unix.go"}, {"windows", "lock_windows.go"}, {"plan9", ""},
	} {
		ctx := build.Default
		ctx.GOOS = c.goos
		pkg, err := ctx.ImportDir(".", 0)
		if err != nil {
			t.Fatalf("%s: %v", c.goos, err)
		}
		for _, f := range []string{"lock_unix.go", "lock_windows.go"} {
			if got := slices.Contains(pkg.GoFiles, f); got != (f == c.lock) {
				t.Errorf("%s builds %v: %s included is %v", c.goos, pkg.GoFiles, f, got)
			}
		}
	}
}
