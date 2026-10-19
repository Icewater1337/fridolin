package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// The check/open interval is explicit: no racing goroutine or global hook is
// needed to show whether a replaced candidate can expose another directory.
func TestInspectRejectsReplacedCanonicalComponents(t *testing.T) {
	for _, replacement := range []string{"candidate", "parent"} {
		for _, destination := range []string{"outside-home", "protected-in-home"} {
			t.Run(replacement+"/"+destination, func(t *testing.T) {
				g, homePath := testBrainRegistry(t)
				candidate := filepath.Join(homePath, "candidate", "nested")
				if err := os.MkdirAll(candidate, 0o700); err != nil {
					t.Fatal(err)
				}
				abs, problems, _ := g.checkPath(candidate, g.store.Get(), nil)
				if len(problems) != 0 {
					t.Fatalf("initial candidate rejected: %v", problems)
				}
				target := t.TempDir()
				if destination == "protected-in-home" {
					target = g.store.Get().DataDir
				}
				entry := candidate
				if replacement == "parent" {
					entry = filepath.Dir(candidate)
					target = filepath.Join(target, "nested")
				}
				if err := os.MkdirAll(target, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := manifest.Write(target, inspectionManifest("private"), false); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(entry, entry+".original"); err != nil {
					t.Fatal(err)
				}
				linkTarget, err := filepath.Rel(filepath.Dir(entry), target)
				if replacement == "parent" {
					linkTarget, err = filepath.Rel(filepath.Dir(entry), filepath.Dir(target))
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(linkTarget, entry); err != nil {
					t.Fatal(err)
				}
				home, err := os.OpenRoot(fsx.Canonical(homePath))
				if err != nil {
					t.Fatal(err)
				}
				defer home.Close()
				got := &Inspection{}
				g.inspectFolderMetadata(home, abs, got)
				if got.Exists == nil || *got.Exists || got.IsDir == nil || *got.IsDir || got.Entries != nil || got.Manifest != nil {
					t.Fatalf("inspected replaced candidate: %+v", got)
				}
			})
		}
	}
}

func TestInspectDetectsReplacementDuringOpen(t *testing.T) {
	for _, replacement := range []string{"directory", "relative-symlink"} {
		t.Run(replacement, func(t *testing.T) {
			home := t.TempDir()
			for _, name := range []string{"candidate", "private"} {
				if err := os.Mkdir(filepath.Join(home, name), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(home)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			observed, err := root.Lstat("candidate")
			if err != nil {
				t.Fatal(err)
			}
			if err := root.Rename("candidate", "original"); err != nil {
				t.Fatal(err)
			}
			if replacement == "directory" {
				err = root.Rename("private", "candidate")
			} else {
				err = root.Symlink("private", "candidate")
			}
			if err != nil {
				t.Fatal(err)
			}
			if dir, err := openInspectChild(root, "candidate", observed); err == nil {
				dir.Close()
				t.Fatal("accepted a different directory after the metadata check")
			}
		})
	}
}

func TestInspectManifestKeepsPinnedFolder(t *testing.T) {
	home := t.TempDir()
	for _, id := range []string{"candidate", "private"} {
		dir := filepath.Join(home, id)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := manifest.Write(dir, inspectionManifest(id), false); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(fsx.Canonical(home))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	dir, _, err := openInspectFolder(root, filepath.Join(root.Name(), "candidate"))
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := root.Rename("candidate", "original"); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink("private", "candidate"); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.ReadRoot(dir)
	if err != nil || m.ID != "candidate" {
		t.Fatalf("pinned manifest = %+v, %v", m, err)
	}
}

func inspectionManifest(id string) *manifest.Manifest {
	return &manifest.Manifest{ID: id, Name: id}
}

func TestInspectPreservesAcceptedAliasesAndFileMetadata(t *testing.T) {
	g, home := testBrainRegistry(t)
	brain := filepath.Join(home, "candidate", "nested")
	if err := os.MkdirAll(brain, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(brain, inspectionManifest("candidate"), false); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(brain), filepath.Join(home, "alias")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, "ordinary-file")
	if err := os.WriteFile(file, []byte("ordinary"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path          string
		exists, isDir bool
	}{
		{brain, true, true},
		{filepath.Join(home, "alias", "nested"), true, true},
		{file, true, false},
		{filepath.Join(home, "missing"), false, false},
	} {
		got, err := g.Inspect(tc.path)
		if err != nil || got.Exists == nil || *got.Exists != tc.exists || got.IsDir == nil || *got.IsDir != tc.isDir {
			t.Fatalf("inspect %s = %+v, %v", tc.path, got, err)
		}
		if tc.isDir {
			m := got.Manifest
			if got.Entries == nil || *got.Entries != 1 || m == nil || m.ID != "candidate" {
				t.Fatalf("lost directory metadata: %+v", got)
			}
		}
	}
}
