package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONIsStrict(t *testing.T) {
	type body struct {
		DryRun bool   `json:"dry_run"`
		Name   string `json:"name"`
	}
	cases := []struct {
		name     string
		body     string
		chunked  bool
		optional bool
		want     string // "" = decodes
	}{
		{name: "one value", body: `{"dry_run": true, "name": "x"}`},
		{name: "trailing space", body: `{"dry_run": true}` + "\n  "},
		{name: "unknown key", body: `{"dry_run": true, "dryrun": false}`, want: `unknown field "dryrun"`},
		{name: "second value", body: `{"dry_run": true} {"garbage": 1}`, want: "single JSON value"},
		{name: "wrong type", body: `{"dry_run": "yes"}`, want: "dry_run"},
		{name: "cut short", body: `{"dry_run": tr`, want: "invalid JSON"},
		{name: "empty", body: ``, want: "a JSON body is required"},
		{name: "empty chunked", body: ``, chunked: true, want: "a JSON body is required"},
		{name: "empty optional", body: ``, optional: true},
		{name: "empty optional chunked", body: ``, chunked: true, optional: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader(c.body))
			if c.chunked {
				r.ContentLength = -1
			}
			var v body
			var err error
			if c.optional {
				err = decodeOptionalJSON(r, &v)
			} else {
				err = decodeJSON(r, &v)
			}
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("got %v, want it decoded", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("got %v, want an error with %q", err, c.want)
			}
		})
	}
}
