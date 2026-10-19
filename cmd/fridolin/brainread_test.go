package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

func TestReadAtMostKeepsSizeAndReaderErrors(t *testing.T) {
	data := bytes.Repeat([]byte{'a'}, maxBrainFile)
	if got, err := readAtMost(bytes.NewReader(data)); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("exact limit: len=%d, err=%v", len(got), err)
	}
	if got, err := readAtMost(io.MultiReader(bytes.NewReader(data), strings.NewReader("b"))); got != nil || err == nil || err.Error() != "larger than 256 KiB" {
		t.Fatalf("over limit: len=%d, err=%v", len(got), err)
	}
	if got, err := readAtMost(brokenBrainReader{}); got != nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("reader failure: %q, %v", got, err)
	}
}

type brokenBrainReader struct{}

func (brokenBrainReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestReadBrainFileRefusesNonRegularAndEscapingEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "regular"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("regular", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readBrainFile(dir, "regular"); err != nil || string(got) != "inside" {
		t.Fatalf("regular file: %q, %v", got, err)
	}
	for _, name := range []string{"link", "pipe", ".", "../outside", "missing"} {
		if _, err := readBrainFile(dir, name); err == nil {
			t.Errorf("%q was accepted", name)
		} else if name == "link" && !errors.Is(err, fsx.ErrSymlink) {
			t.Errorf("link error = %v", err)
		}
	}
	if _, err := readBrainFile(filepath.Join(dir, "missing"), "file"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing root: %v", err)
	}
}

func TestReadBrainFileRootStaysOnThePinnedFolder(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "brain")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readBrainFileRoot(root, "file"); err != nil || string(got) != "original" {
		t.Fatalf("pinned read: %q, %v", got, err)
	}
}
