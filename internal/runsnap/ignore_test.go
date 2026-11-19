package runsnap

import "testing"

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
