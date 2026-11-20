package runsnap

import (
	"slices"
	"strings"
	"testing"
)

func TestIgnoreRules(t *testing.T) {
	t.Parallel()
	ig := ignoreRules{
		"": parseIgnore([]byte("# comment\n\n*.log\n!keep.log\n/top.txt\nbuild/\ndocs/**/draft.md\n\\#hash\n[bad\n" +
			"a/**/**/**/**/**/b\n")),
		"sub": parseIgnore([]byte("local.txt\n!top.txt\n")),
	}
	for _, tc := range []struct {
		rel  string
		dir  bool
		want bool
	}{
		{"x.log", false, true},
		{"deep/x.log", false, true},
		{"keep.log", false, false},
		{"top.txt", false, true},
		{"sub/top.txt", false, false},
		{"other/top.txt", false, false},
		{"build", true, true},
		{"build", false, false},
		{"src/build", true, true},
		{"docs/draft.md", false, true},
		{"docs/a/b/draft.md", false, true},
		{"draft.md", false, false},
		{"#hash", false, true},
		{"sub/local.txt", false, true},
		{"local.txt", false, false},
		{"sub/x/local.txt", false, true},
		{"a/x/b", false, false}, // too many "**": the rule is dropped
		{"notes.md", false, false},
	} {
		if got := ig.ignored(tc.rel, tc.dir); got != tc.want {
			t.Errorf("ignored(%q, %v) = %v, want %v", tc.rel, tc.dir, got, tc.want)
		}
	}
	if (ignoreRules{}).ignored("x", false) {
		t.Error("no rules ignored something")
	}
}

// TestIgnoreLikeGit: what git check-ignore (git 2.x, --no-index) says
// for each .gitignore and path; "build" asked as a folder.
func TestIgnoreLikeGit(t *testing.T) {
	t.Parallel()
	paths := []struct {
		rel string
		dir bool
	}{
		{"main.c", false}, {"x.o", false}, {"abc", false}, {"xbc", false},
		{"build", true}, {"build/keep.txt", false}, {"build/x.txt", false},
		{"build/sub", true}, {"build/sub/keep.txt", false}, {"!x", false}, {"[x", false},
	}
	for _, tc := range []struct {
		gitignore string
		ignored   []string
	}{
		{"*.[!o]\n", []string{"main.c"}},
		{"[!a]bc\n", []string{"xbc"}},
		{"build/**\n!build/keep.txt\n", []string{"build/x.txt", "build/sub", "build/sub/keep.txt"}},
		{"build/\n", []string{"build", "build/keep.txt", "build/x.txt", "build/sub", "build/sub/keep.txt"}},
		{"build\n", []string{"build", "build/keep.txt", "build/x.txt", "build/sub", "build/sub/keep.txt"}},
		{"[[!]x\n", []string{"!x", "[x"}}, // "!" inside a class stays literal
	} {
		ig := ignoreRules{"": parseIgnore([]byte(tc.gitignore))}
		for _, p := range paths {
			// As the walk does: nothing below an ignored folder is looked at.
			got := false
			segs := strings.Split(p.rel, "/")
			for i := 1; i <= len(segs) && !got; i++ {
				got = ig.ignored(strings.Join(segs[:i], "/"), i < len(segs) || p.dir)
			}
			if want := slices.Contains(tc.ignored, p.rel); got != want {
				t.Errorf("%q: %s ignored = %v, git says %v", tc.gitignore, p.rel, got, want)
			}
		}
	}
	if r := parseIgnore([]byte("[!x]\n")); len(r) != 1 || r[0].segs[1] != "[^x]" {
		t.Errorf("[!x] parsed as %+v", r)
	}
}

// FuzzIgnore: a .gitignore is a file a brain writes and the bridge reads
// outside its sandbox. Whatever it holds, parsing it stays within its
// bounds, and matching a path against it does not panic.
func FuzzIgnore(f *testing.F) {
	f.Add("*.log\n!keep.log\n/top/\nbuild/\n", "a/b/x.log")
	f.Add("**/**/**/**/a\n[z-a]\n\\!x\n", "a/a/a/a/a")
	f.Add("a/**/b/**/c/**/d\n", "a/x/b/y/c/z/d")
	f.Fuzz(func(t *testing.T, data, rel string) {
		rules := parseIgnore([]byte(data))
		if len(rules) > maxIgnoreRules {
			t.Fatalf("%d rules", len(rules))
		}
		for _, r := range rules {
			stars := 0
			for _, s := range r.segs {
				if s == "**" {
					stars++
				}
			}
			// One "**" more than the bound: the one an unanchored rule
			// starts with.
			if len(r.segs) == 0 || stars > maxIgnoreStars+1 || len(r.segs) > maxIgnoreSegment+1 {
				t.Fatalf("a rule out of bounds: %+v", r)
			}
		}
		ig := ignoreRules{"": rules, "a": rules}
		_ = ig.ignored(rel, false)
		_ = ig.ignored(rel, true)
	})
}
