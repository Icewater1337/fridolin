package main

import (
	"os"
	"path/filepath"
	"sync"
)

// bridgeLayout is where the bridge's own files are, found from the path of
// its binary.
type bridgeLayout struct {
	// Dir is the folder that holds tools/ next to the binary: the root of
	// the source checkout for a binary built there. The bridge runs what
	// is in it outside any sandbox.
	Dir string
	// Tools is the folder with the Python helpers brains run.
	Tools string
	// Source is the source checkout the binary was built in, or "". The
	// next setup.sh run builds what is in it.
	Source string
}

// layoutOf finds the layout around the binary exe: tools/ next to it, or
// in the checkout above its bin/ (a dev build). It is empty for a binary
// on its own.
func layoutOf(exe string) bridgeLayout {
	dir := filepath.Dir(exe)
	switch {
	case isDir(filepath.Join(dir, "tools")):
	case filepath.Base(dir) == "bin" && isCheckout(filepath.Dir(dir)):
		dir = filepath.Dir(dir)
	default:
		return bridgeLayout{}
	}
	l := bridgeLayout{Dir: dir, Tools: filepath.Join(dir, "tools")}
	if isCheckout(dir) {
		l.Source = dir
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
