package main

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGoldenAssetInventory pins what the dashboard serves under /: every
// file embedded from web/, with the status and content type it is served
// with, and a few files of web/ that are not part of the page. A file
// served is checked against the checkout's copy byte for byte, so the
// golden names the files, not their contents: a change to the dashboard's
// code changes it only when a file of the page comes or goes.
func TestGoldenAssetInventory(t *testing.T) {
	h := newHarness(t, "")
	var sb strings.Builder
	check := func(urlPath string, want []byte) {
		w := h.Do("GET", urlPath, nil, func(r *http.Request) { r.Header.Del("X-Bridge-Token") })
		fmt.Fprintln(&sb, strings.TrimSpace(fmt.Sprintf("%s %d %s", urlPath, w.Code, w.Header().Get("Content-Type"))))
		if w.Code == http.StatusOK && want != nil && w.Body.String() != string(want) {
			t.Errorf("GET %s serves other bytes than the checkout's file", urlPath)
		}
	}
	checkout := func(path string) []byte {
		data, err := os.ReadFile(filepath.Join(repoDir, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	check("/", checkout("web/index.html"))
	err := fs.WalkDir(webFS, "web", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		check(strings.TrimPrefix(path, "web"), checkout(path))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The JavaScript tests, and the package.json node reads for them.
	for _, p := range []string{"/package.json", "/test/serve.js"} {
		check(p, nil)
	}
	assertGolden(t, "api/assets.golden", []byte(sb.String()))
}
