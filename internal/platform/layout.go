//go:build unix

package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// bridgeLayout is where the bridge's own files are, found from the path of
// its binary.
type BridgeLayout struct {
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
const InstallManifestFile = "install.json"

type InstallManifest struct {
	Version     string    `json:"version"`
	Source      string    `json:"source,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
}

func ReadInstallManifest(dir string) (InstallManifest, error) {
	var m InstallManifest
	data, err := os.ReadFile(filepath.Join(dir, InstallManifestFile))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(data, &m)
}

// layoutOf finds the layout around the binary exe: an install or tools/
// next to it, or the checkout above its bin/ (a dev build). It is empty
// for a binary on its own.
func LayoutOf(exe string) BridgeLayout {
	dir := filepath.Dir(exe)
	switch {
	// install.json as well: a binary installed without tools leaves the
	// install folder with no tools/, and the sandbox must still keep
	// brains from writing in it.
	case IsDir(filepath.Join(dir, "tools")), pathExists(filepath.Join(dir, InstallManifestFile)):
	case filepath.Base(dir) == "bin" && IsCheckout(filepath.Dir(dir)):
		dir = filepath.Dir(dir)
	default:
		return BridgeLayout{}
	}
	l := BridgeLayout{Dir: dir}
	if IsDir(filepath.Join(dir, "tools")) {
		l.Tools = filepath.Join(dir, "tools")
	}
	if IsCheckout(dir) {
		l.Source = dir
	} else if m, err := ReadInstallManifest(dir); err == nil && filepath.IsAbs(m.Source) && IsCheckout(m.Source) {
		l.Source = filepath.Clean(m.Source)
	}
	return l
}

// isCheckout reports whether dir is the root of a source checkout.
func IsCheckout(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil && st.Mode().IsRegular() && IsDir(filepath.Join(dir, "tools"))
}

func IsDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// pathExists includes dangling symlinks, matching installation layout discovery.
func pathExists(p string) bool { _, err := os.Lstat(p); return err == nil }
