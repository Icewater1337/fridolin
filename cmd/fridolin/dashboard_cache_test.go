package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONValidatorPreservesBodyAndWeakComparison(t *testing.T) {
	const etag = `"e8bf345c795d7515dc1471d3"`
	for _, c := range []struct {
		header string
		status int
	}{
		{"", http.StatusOK},
		{`"stale"`, http.StatusOK},
		{etag, http.StatusNotModified},
		{"W/" + etag, http.StatusNotModified},
		{`"stale", W/` + etag, http.StatusNotModified},
		{"*", http.StatusNotModified},
	} {
		t.Run(c.header, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Header.Set("If-None-Match", c.header)
			w := httptest.NewRecorder()
			writeJSONIfChanged(w, r, map[string]any{"z": nil, "a": []string{"one"}})
			if w.Code != c.status || w.Header().Get("ETag") != etag {
				t.Fatalf("response = %d %v", w.Code, w.Header())
			}
			if c.status == http.StatusNotModified {
				if w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
					t.Fatalf("304 carries body or content type: %q %v", w.Body.String(), w.Header())
				}
			} else if w.Body.String() != "{\"a\":[\"one\"],\"z\":null}\n" || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("JSON response changed: %q %v", w.Body.String(), w.Header())
			}
		})
	}
}

func TestJSONValidatorReportsMarshalFailureBeforeHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSONIfChanged(w, httptest.NewRequest(http.MethodGet, "/", nil), make(chan int))
	if w.Code != http.StatusInternalServerError || w.Header().Get("ETag") != "" || !strings.Contains(w.Body.String(), "json: unsupported type: chan int") {
		t.Fatalf("marshal failure = %d %v %s", w.Code, w.Header(), w.Body)
	}
}
