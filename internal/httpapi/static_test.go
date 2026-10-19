package httpapi

import (
	"io/fs"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestStaticFilesServeAnInjectedFilesystem(t *testing.T) {
	h := StaticHandler(fstest.MapFS{
		"index.html":     {Data: []byte("page")},
		"assets/app.txt": {Data: []byte("hello")},
	})
	for _, c := range []struct {
		path, method, match  string
		status               int
		body, etag, location string
	}{
		{"/", "GET", "", 200, "page", `"3660315a9af3df255d8f19ab"`, ""},
		{"/assets/app.txt", "GET", "", 200, "hello", `"2cf24dba5fb0a30e26e83b2a"`, ""},
		{"/assets/app.txt", "HEAD", "", 200, "", `"2cf24dba5fb0a30e26e83b2a"`, ""},
		{"/assets/app.txt", "GET", `W/"2cf24dba5fb0a30e26e83b2a"`, 304, "", `"2cf24dba5fb0a30e26e83b2a"`, ""},
		{"/assets/", "GET", "", 404, "404 page not found\n", "", ""},
		{"/missing", "GET", "", 404, "404 page not found\n", "", ""},
		{"/index.html", "GET", "", 301, "", "", "./"},
		{"/assets/app.txt/", "GET", "", 301, "", "", "../app.txt"},
	} {
		t.Run(c.method+" "+c.path+c.match, func(t *testing.T) {
			r := httptest.NewRequest(c.method, c.path, nil)
			r.Header.Set("If-None-Match", c.match)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.status || w.Body.String() != c.body || w.Header().Get("ETag") != c.etag || w.Header().Get("Location") != c.location {
				t.Fatalf("response = %d %v %q", w.Code, w.Header(), w.Body.String())
			}
			if c.etag != "" && w.Header().Get("Cache-Control") != "no-cache" {
				t.Fatal("file validator lost revalidation policy")
			}
		})
	}
}

func TestStaticFilesRejectUnreadableEmbeddedRoot(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("invalid filesystem silently accepted")
		}
	}()
	StaticHandler(unreadableDashboardFS{})
}

type unreadableDashboardFS struct{}

func (unreadableDashboardFS) Open(string) (fs.File, error) { return nil, fs.ErrPermission }
