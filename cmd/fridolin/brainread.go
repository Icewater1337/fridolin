package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// maxBrainFile bounds what the bridge reads of the files a brain can
// write: its .mcp.json, settings, prompt, identity map and calendar token.
// That is far more than any real one holds, so that a brain cannot make
// the bridge load gigabytes into memory on every run, and little enough to
// pass a .mcp.json on the command line.
const maxBrainFile = 256 << 10

// readAtMost reads r to the end, and refuses it when that is more than
// maxBrainFile.
func readAtMost(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBrainFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBrainFile {
		return nil, fmt.Errorf("larger than %d KiB", maxBrainFile>>10)
	}
	return data, nil
}

// readBrainFile reads the file name, relative to the brain folder dir, as
// openInFolder opens it. It refuses a file larger than maxBrainFile.
func readBrainFile(dir, name string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return readBrainFileRoot(root, name)
}

// readBrainFileRoot keeps a transaction's reads on its already pinned folder.
func readBrainFileRoot(root *os.Root, name string) ([]byte, error) {
	f, err := fsx.OpenInFolder(root, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readAtMost(f)
}
