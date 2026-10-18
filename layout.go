package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// bridgeLayout is where the bridge's own files are, found from the path of
// its binary.
type bridgeLayout struct {
	// Dir is the folder the binary came with: the install folder `fridolin
	// service install` made (it holds install.json, and tools/ unless the
	// binary was installed without them), the root of the source checkout
	// for a binary built there, or a folder with tools/ next to the binary.
	// The bridge runs what is in it outside any sandbox.
	Dir string
	// Tools is the folder with the Python helpers brains run, or "" when
	// Dir has none.
	Tools string
	// Source is the source checkout the binary was built in or installed
	// from, or "" (a release archive, say). The next setup.sh run builds
	// what is in it.
	Source string
}

// installManifestFile, in the install folder, records what was installed
// and from where.
const installManifestFile = "install.json"

type installManifest struct {
	Version     string    `json:"version"`
	Source      string    `json:"source,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
}

func readInstallManifest(dir string) (installManifest, error) {
	var m installManifest
	data, err := os.ReadFile(filepath.Join(dir, installManifestFile))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(data, &m)
}

// layoutOf finds the layout around the binary exe: an install or tools/
// next to it, or the checkout above its bin/ (a dev build). It is empty
// for a binary on its own.
func layoutOf(exe string) bridgeLayout {
	dir := filepath.Dir(exe)
	switch {
	// install.json as well: a binary installed without tools leaves the
	// install folder with no tools/, and the sandbox must still keep
	// brains from writing in it.
	case isDir(filepath.Join(dir, "tools")), pathExists(filepath.Join(dir, installManifestFile)):
	case filepath.Base(dir) == "bin" && isCheckout(filepath.Dir(dir)):
		dir = filepath.Dir(dir)
	default:
		return bridgeLayout{}
	}
	l := bridgeLayout{Dir: dir}
	if isDir(filepath.Join(dir, "tools")) {
		l.Tools = filepath.Join(dir, "tools")
	}
	if isCheckout(dir) {
		l.Source = dir
	} else if m, err := readInstallManifest(dir); err == nil && filepath.IsAbs(m.Source) && isCheckout(m.Source) {
		l.Source = filepath.Clean(m.Source)
	}
	return l
}

// isCheckout reports whether dir is the root of a source checkout.
func isCheckout(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil && st.Mode().IsRegular() && isDir(filepath.Join(dir, "tools"))
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// ownLayout is the running bridge's layout; tests replace it.
var ownLayout = sync.OnceValue(func() bridgeLayout {
	exe, err := os.Executable()
	if err != nil {
		return bridgeLayout{}
	}
	return layoutOf(exe)
})
