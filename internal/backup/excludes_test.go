package backup

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestTranslateExclude is the translation of rclone's filter globs into
// patterns anchored at the brain folder, as restic matches them.
func TestTranslateExclude(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"/work/", []string{"work"}}, // anchored folder
		{"/work-clones/", []string{"work-clones"}},
		{"node_modules/", []string{"**/node_modules"}}, // folder at any depth
		{"*.log", []string{"**/*.log"}},                // glob at any depth
		{".DS_Store", []string{"**/.DS_Store"}},        // name at any depth
		{"/notes/private/", []string{"notes/private"}}, // nested, anchored
		{"cache/tmp/", []string{"**/cache/tmp"}},       // nested, at any depth
		{"/exports/*.zip", []string{"exports/*.zip"}},  // anchored glob
		{"/exports/**.zip", []string{"exports/*.zip", "exports/*/**/*.zip"}},
		{"/data/**", []string{"data"}},                    // FilterLines' form of "/data/"
		{"- /old/", []string{"old"}},                      // a rule's "- " prefix
		{"*.{jpg,png}", []string{"**/*.jpg", "**/*.png"}}, // alternatives
		{"/a/**/b/", []string{"a/**/b"}},
		{"**", []string{"**"}},     // everything
		{"/**", []string{"**"}},    // everything
		{"{{.*}}", []string{"**"}}, // rclone's regexp: left out whole
		{"/bad[/", []string{"**"}}, // a bad pattern: left out whole
		{"", nil},
		{"   ", nil},
		{"+ /keep/", nil}, // an include can never let a file back in
		{"x\ny", nil},
	} {
		got := translateExclude(tc.in)
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("translateExclude(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestExcluded decides what a brain's excludes leave out, as restic does
// with the same patterns.
func TestExcluded(t *testing.T) {
	owner := []string{"node_modules/", "*.log", ".DS_Store", ".venv/", "__pycache__/", "/work/", "/work-clones/", "/exports/**.zip"}
	pats := ExcludePatterns(owner, false)
	for rel, want := range map[string]bool{
		"work":                        true,
		"work/":                       true,
		"work/contract.pdf":           true,
		"work/deep/er/x.md":           true,
		"work-clones/a/b.docx":        true,
		"memories/work/x.md":          false, // "/work/" is anchored
		"workshop/x.md":               false,
		"work.md":                     false,
		"memories/a.md":               false,
		"brain.json":                  false,
		"app/node_modules/x/index.js": true,
		"node_modules":                true,
		"a/b/c/run.log":               true,
		"run.log":                     true,
		"logs/run.txt":                false,
		"x/.DS_Store":                 true,
		"exports/a.zip":               true,
		"exports/d/e/b.zip":           true,
		"exports/c.txt":               false,
		"other/exports/a.zip":         false,
		".env":                        true, // the secret files
		"tools/.env.local":            true,
		"keys/id_ed25519.pub":         true,
		"google_calendar_token.json":  true,
		"env.md":                      false,
		"":                            false,
		"./work/x":                    true,
		"memories/../work/x":          true,
	} {
		if got := Excluded(rel, pats); got != want {
			t.Errorf("Excluded(%q) = %v, want %v", rel, got, want)
		}
	}
	// include_secret_files lets the secret files through, and only them.
	pats = ExcludePatterns(owner, true)
	if Excluded(".env", pats) || !Excluded("work/x", pats) {
		t.Error("include_secret_files: .env should go, work/ should not")
	}
	if !Excluded("anything/at/all", ExcludePatterns([]string{"/**"}, true)) {
		t.Error("/** leaves out the whole folder")
	}
}

// TestResticExcludes: the patterns become absolute under their brain's
// folder, with the folder's own pattern characters escaped, so one brain's
// excludes never touch another brain's files.
func TestResticExcludes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("restic takes no escapes on Windows")
	}
	got := ResticExcludes("/Users/you/brains/a", ExcludePatterns([]string{"/work/", "*.log"}, true))
	want := []string{"/Users/you/brains/a/**/*.log", "/Users/you/brains/a/work"}
	if !slices.Equal(got, want) {
		t.Errorf("ResticExcludes = %q, want %q", got, want)
	}
	if got := ResticPath("/x/b[1]*?/c\\d/"); got != `/x/b\[1\]\*\?/c\\d` {
		t.Errorf("ResticPath = %q", got)
	}
	if got := ResticExcludes("/x/a", []string{"**"}); !slices.Equal(got, []string{"/x/a"}) {
		t.Errorf("the whole folder: %q", got)
	}
	for _, p := range ResticExcludes("/brains/a", ExcludePatterns(nil, false)) {
		if !strings.HasPrefix(p, "/brains/a"+string(filepath.Separator)) {
			t.Errorf("%q is not under its brain folder", p)
		}
	}
}
