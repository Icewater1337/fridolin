package main

import "runtime/debug"

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

// bridgeVersion returns the release version, or the git commit for a local build.
func bridgeVersion() string {
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
