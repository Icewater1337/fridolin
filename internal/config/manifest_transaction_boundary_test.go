package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

func stagedManifestForTest(t *testing.T, create bool) (*Tx, string, []byte) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "brain")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var before []byte
	if !create {
		if err := manifest.Write(dir, &manifest.Manifest{ID: "a", Name: "Before"}, false); err != nil {
			t.Fatal(err)
		}
		before, _ = os.ReadFile(filepath.Join(dir, manifest.File))
	}
	tx := &Tx{}
	t.Cleanup(tx.close)
	if err := tx.stage(dir, &manifest.Manifest{ID: "a", Name: "During"}, create); err != nil {
		t.Fatal(err)
	}
	if err := tx.write(); err != nil {
		t.Fatal(err)
	}
	return tx, dir, before
}

func TestManifestRollbackKeepsTheOriginalFolder(t *testing.T) {
	for _, create := range []bool{false, true} {
		t.Run(fmt.Sprintf("create=%t", create), func(t *testing.T) {
			tx, dir, before := stagedManifestForTest(t, create)
			moved := dir + "-moved"
			if err := os.Rename(dir, moved); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			outsideFile := filepath.Join(outside, manifest.File)
			const keep = "a file outside the transaction\n"
			if err := os.WriteFile(outsideFile, []byte(keep), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, dir); err != nil {
				t.Fatal(err)
			}
			tx.rollback()
			if got, err := os.ReadFile(outsideFile); err != nil || string(got) != keep {
				t.Errorf("rollback changed the replacement directory: %q, %v", got, err)
			}
			got, err := os.ReadFile(filepath.Join(moved, manifest.File))
			if create {
				if !os.IsNotExist(err) {
					t.Errorf("created manifest was not removed from the original directory: %v", err)
				}
			} else if err != nil || !bytes.Equal(got, before) {
				t.Errorf("original manifest was not restored: %q, %v", got, err)
			}
		})
	}
}

func TestManifestRollbackPreservesChangedFile(t *testing.T) {
	for _, create := range []bool{false, true} {
		for _, replace := range []bool{false, true} {
			t.Run(fmt.Sprintf("create=%t/replace=%t", create, replace), func(t *testing.T) {
				tx, dir, _ := stagedManifestForTest(t, create)
				path := filepath.Join(dir, manifest.File)
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				keep, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if replace {
					// Even identical bytes now belong to a different file.
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				} else {
					// Metadata alone is insufficient: preserve an in-place edit
					// with the same length and restored modification time too.
					keep = bytes.Replace(keep, []byte("During"), []byte("Edited"), 1)
				}
				if err := os.WriteFile(path, keep, info.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				tx.rollback()
				if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, keep) {
					t.Errorf("rollback changed another writer's manifest: %q, %v", got, err)
				}
			})
		}
	}
}

func TestManifestWriteStaysInPinnedFolder(t *testing.T) {
	for _, create := range []bool{false, true} {
		t.Run(fmt.Sprintf("create=%t", create), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "brain")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if !create {
				if err := manifest.Write(dir, &manifest.Manifest{ID: "a", Name: "Before"}, false); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			moved := dir + "-moved"
			if err := os.Rename(dir, moved); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			outsideFile := filepath.Join(outside, manifest.File)
			const keep = "outside\n"
			if err := os.WriteFile(outsideFile, []byte(keep), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, dir); err != nil {
				t.Fatal(err)
			}
			data, err := manifest.Encode(&manifest.Manifest{ID: "a", Name: "After"})
			if err != nil {
				t.Fatal(err)
			}
			file, _, err := manifest.WriteDataRoot(root, data, !create)
			if err != nil {
				t.Fatal(err)
			}
			file.Close()
			if got, err := os.ReadFile(filepath.Join(moved, manifest.File)); err != nil || !bytes.Equal(got, data) {
				t.Errorf("write missed the pinned folder: %q, %v", got, err)
			}
			if got, err := os.ReadFile(outsideFile); err != nil || string(got) != keep {
				t.Errorf("write changed the replacement folder: %q, %v", got, err)
			}
			entries, err := os.ReadDir(moved)
			if err != nil || len(entries) != 1 || entries[0].Name() != manifest.File {
				t.Errorf("temporary file cleanup missed the pinned folder: %v, %v", entries, err)
			}
		})
	}
}

func TestManifestTransactionReleasesHandles(t *testing.T) {
	tx, _, _ := stagedManifestForTest(t, true)
	root, file := tx.writes[0].root, tx.writes[0].file
	tx.close()
	if _, err := root.Stat(manifest.File); err == nil {
		t.Error("transaction left its folder handle open")
	}
	if _, err := file.Stat(); err == nil {
		t.Error("transaction left its manifest handle open")
	}
}
