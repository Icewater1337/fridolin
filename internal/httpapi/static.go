package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
)

// StaticHandler serves the page's files from root. http.FileServer alone
// lists the files of a folder (GET /assets/), and embedded files have no
// modification time, so it sends no validator and the browser downloads
// every module again on each visit. Here a folder is not found, and each
// file gets an ETag from its content with Cache-Control: no-cache: the
// browser asks each time, which keeps an upgraded bridge's files from
// being mixed with cached old ones, and the answer is a 304 for every
// file the upgrade did not change.
func StaticHandler(root fs.FS) http.Handler {
	etags := map[string]string{}
	err := fs.WalkDir(root, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(root, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		etags["/"+name] = `"` + hex.EncodeToString(sum[:12]) + `"`
		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("embedded dashboard: %v", err))
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean(r.URL.Path)
		if name == "/" {
			name = "/index.html"
		}
		etag, ok := etags[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		// The file server answers /index.html and a file's name with a
		// slash after it with a redirect to the file's own address, which
		// the file's validator does not describe.
		if r.URL.Path == "/" || (r.URL.Path == name && name != "/index.html") {
			w.Header().Set("ETag", etag)
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
