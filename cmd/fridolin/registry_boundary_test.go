package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

func TestResolutionSecurityCheckOrderAndIssueContext(t *testing.T) {
	t.Parallel()
	home := fsx.Canonical(t.TempDir())
	refs := []config.BrainRef{
		{Path: filepath.Join(home, "unapproved")},
		{Path: filepath.Join(home, "bad"), Approved: &manifest.Manifest{ID: "bad"}},
		{Path: filepath.Join(home, "missing"), Approved: &manifest.Manifest{ID: "missing", Name: "Missing"}},
		{Path: filepath.Join(home, "blocked"), Approved: &manifest.Manifest{ID: "blocked", Name: "Blocked", Routing: manifest.Routing{HandlesDMs: true}}},
		{Path: filepath.Join(home, "allowed"), Approved: &manifest.Manifest{ID: "allowed", Name: "Allowed"}},
	}
	for _, ref := range refs[3:] {
		if err := os.MkdirAll(ref.Path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var calls []string
	specs, issues := resolveBrainRefs(refs, func(dir, configured string) error {
		if dir != configured {
			t.Errorf("checker paths = %q, %q", dir, configured)
		}
		calls = append(calls, filepath.Base(dir))
		if filepath.Base(dir) == "blocked" {
			return errors.New("is protected")
		}
		return nil
	})
	if !reflect.DeepEqual(calls, []string{"blocked", "allowed"}) {
		t.Fatalf("checks = %v", calls)
	}
	if len(specs) != 1 || specs[0].Manifest.ID != "allowed" || specs[0].Entry != 4 {
		t.Fatalf("specs = %+v", specs)
	}
	if len(issues) != 4 {
		t.Fatalf("issues = %+v", issues)
	}
	is := issues[3]
	if is.Entry != 3 || !is.Routing.HandlesDMs || !is.LeavesOut() || is.Kind != IssueInvalid || is.Detail != "the folder is protected, so it cannot hold a brain; move the brain to a folder of its own" {
		t.Fatalf("blocked = %+v", is)
	}
	is.AtLoad = true
	data, err := json.Marshal(is)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 || fields["kind"] != "invalid" || fields["id"] != "blocked" {
		t.Fatalf("wire issue = %s", data)
	}
	for _, k := range []IssueKind{IssueDrift, IssueUnapproved} {
		if (BrainIssue{ID: "x", Kind: k}).LeavesOut() {
			t.Fatalf("%s incorrectly holds routes", k)
		}
	}
}

func TestBrainPathContextConfinementBeforeSecurityCheck(t *testing.T) {
	t.Parallel()
	home := fsx.Canonical(t.TempDir())
	for _, path := range []string{"relative", home, "/", filepath.Join(filepath.Dir(home), "outside")} {
		_, problems, _ := checkBrainPath(path, BrainPathContext{Home: home, CheckDir: func(string, string, string) error {
			t.Fatal("security checker called before home confinement")
			return nil
		}})
		if len(problems) == 0 {
			t.Errorf("%q accepted", path)
		}
	}
	var got []string
	target := filepath.Join(home, "Documents", "assistant")
	abs, problems, warnings := checkBrainPath(" ~/Documents/assistant ", BrainPathContext{
		Home: home, Others: []string{target},
		CheckDir: func(dir, configured, homeArg string) error {
			got = []string{dir, configured, homeArg}
			return errors.New("is protected")
		},
	})
	if abs != target || !reflect.DeepEqual(got, []string{target, target, home}) {
		t.Fatalf("path/check = %q %v", abs, got)
	}
	want := []string{"this folder is protected, so it cannot hold a brain — pick or create another one", "this folder is already a brain"}
	if !reflect.DeepEqual(problems, want) || len(warnings) != 1 || !strings.Contains(warnings[0], "Privacy & Security") {
		t.Fatalf("problems/warnings = %v / %v", problems, warnings)
	}
}
