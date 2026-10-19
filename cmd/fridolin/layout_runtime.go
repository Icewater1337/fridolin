package main

import (
	"github.com/Icewater1337/fridolin/internal/platform"
	"os"
	"sync"
)

// ownLayout is the running bridge's layout; tests replace it.
var ownLayout = sync.OnceValue(func() platform.BridgeLayout {
	exe, err := os.Executable()
	if err != nil {
		return platform.BridgeLayout{}
	}
	return platform.LayoutOf(exe)
})
