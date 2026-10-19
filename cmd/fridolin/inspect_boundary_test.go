package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// The check/open interval is explicit: no racing goroutine or global hook is
// needed to show whether a replaced candidate can expose another directory.
func TestInspectRejectsReplacedCanonicalComponents(t *testing.T) {
	for _, replacement := range []string{"candidate", "parent"} {
		for _, destination := range []string{"outside-home", "protected-in-home"} {
			t.Run(replacement+"/"+destination, func(t *testing.T) {
				tb := newTestBridge(t, "")
				candidate := filepath.Join(tb.home, "candidate", "nested")
				if err := os.MkdirAll(candidate, 0o700); err != nil {
					t.Fatal(err)
				}
				abs, problems, _ := CheckBrainPath(candidate, tb.cfgs.Get(), nil)
				if len(problems) != 0 {
					t.Fatalf("initial candidate rejected: %v", problems)
				}
				target := t.TempDir()
				if destination == "protected-in-home" {
					target = tb.cfgs.Get().DataDir
				}
				entry := candidate
				if replacement == "parent" {
					entry = filepath.Dir(candidate)
					target = filepath.Join(target, "nested")
				}
				if err := os.MkdirAll(target, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := WriteManifest(target, ptr(testManifest("private")), false); err != nil {
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
				home, err := os.OpenRoot(fsx.Canonical(tb.home))
				if err != nil {
					t.Fatal(err)
				}
				defer home.Close()
				got := map[string]any{}
				inspectFolderMetadata(home, abs, got)
				if got["exists"] != false || got["is_dir"] != false || got["entries"] != nil || got["manifest"] != nil {
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
		if err := WriteManifest(dir, ptr(testManifest(id)), false); err != nil {
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
	m, err := readManifestRoot(dir)
	if err != nil || m.ID != "candidate" {
		t.Fatalf("pinned manifest = %+v, %v", m, err)
	}
}

func TestInspectPreservesAcceptedAliasesAndFileMetadata(t *testing.T) {
	tb := newTestBridge(t, "")
	brain := filepath.Join(tb.home, "candidate", "nested")
	if err := os.MkdirAll(brain, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(brain, ptr(testManifest("candidate")), false); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(brain), filepath.Join(tb.home, "alias")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(tb.home, "ordinary-file")
	if err := os.WriteFile(file, []byte("ordinary"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path          string
		exists, isDir bool
	}{
		{brain, true, true},
		{filepath.Join(tb.home, "alias", "nested"), true, true},
		{file, true, false},
		{filepath.Join(tb.home, "missing"), false, false},
	} {
		w := tb.do("POST", "/api/brains/inspect", map[string]string{"path": tc.path})
		var got struct {
			Exists      bool
			IsDirectory bool `json:"is_dir"`
			Entries     int
			Manifest    *BrainManifest
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || got.Exists != tc.exists || got.IsDirectory != tc.isDir {
			t.Fatalf("inspect %s = %d %s", tc.path, w.Code, w.Body.String())
		}
		if tc.isDir && (got.Entries != 1 || got.Manifest == nil || got.Manifest.ID != "candidate") {
			t.Fatalf("lost directory metadata: %s", w.Body.String())
		}
	}
}
