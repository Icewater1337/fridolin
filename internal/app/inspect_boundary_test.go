package app

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

func TestInspectPreservesAcceptedAliasesAndFileMetadata(t *testing.T) {
	tb := newTestBridge(t, "")
	brain := filepath.Join(tb.home, "candidate", "nested")
	if err := os.MkdirAll(brain, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(brain, ptr(testManifest("candidate")), false); err != nil {
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
			Manifest    *manifest.Manifest
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
