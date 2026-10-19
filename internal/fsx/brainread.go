package fsx

import (
	"fmt"
	"io"
	"os"
)

// MaxBrainFile bounds what the bridge reads of the files a brain can
// write: its .mcp.json, settings, prompt, identity map and calendar token.
// That is far more than any real one holds, so that a brain cannot make
// the bridge load gigabytes into memory on every run, and little enough to
// pass a .mcp.json on the command line.
const MaxBrainFile = 256 << 10

// ReadAtMost reads r to the end, and refuses it when that is more than
// MaxBrainFile.
func ReadAtMost(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxBrainFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxBrainFile {
		return nil, fmt.Errorf("larger than %d KiB", MaxBrainFile>>10)
	}
	return data, nil
}

// ReadBrainFile reads the file name, relative to the brain folder dir, as
// OpenInFolder opens it. It refuses a file larger than MaxBrainFile.
func ReadBrainFile(dir, name string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return ReadBrainFileRoot(root, name)
}

// ReadBrainFileRoot keeps a transaction's reads on its already pinned folder.
func ReadBrainFileRoot(root *os.Root, name string) ([]byte, error) {
	f, err := OpenInFolder(root, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadAtMost(f)
}
