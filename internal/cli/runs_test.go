package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestRunsUsageGolden(t *testing.T) {
	assertGolden(t, "cli/runs-usage.golden", []byte(runsUsage))
}

func TestRunsCmdArguments(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{nil, 2, "fridolin runs list"},
		{[]string{"--help"}, 0, "fridolin runs undo RUN"},
		{[]string{"list", "extra"}, 2, "takes 0 argument(s)"},
		{[]string{"undo"}, 2, "takes 1 argument(s)"},
		{[]string{"show", "r1", "r2"}, 2, "takes 1 argument(s)"},
		{[]string{"undo", "r1", "--yes", "--config", "/nonexistent/config.json"}, 1, "loading config"},
	} {
		var out, errb bytes.Buffer
		code := runsCmd(tc.args, &out, &errb)
		if code != tc.code || !strings.Contains(out.String()+errb.String(), tc.want) {
			t.Errorf("runs %q: exit %d, out %q, err %q", tc.args, code, out.String(), errb.String())
		}
	}
}

func TestRunsText(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	var b bytes.Buffer
	printRunFilesList(&b, []runFilesItem{
		{Run: "rAb12Cd34Ef5", Brain: "taxes", Ended: now.Add(-12 * time.Minute), Summary: "1 created: notes.md"},
		{Run: "rXy98Zw76Vu5", Brain: "home", Ended: now.Add(-2 * time.Hour), Summary: "1 deleted: list.md", Undone: &now},
	}, now)
	for _, want := range []string{"RUN", "rAb12Cd34Ef5  taxes  12m0s ago", "1 deleted: list.md (undone)"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("list:\n%s\nlacks %q", b.String(), want)
		}
	}
	b.Reset()
	printRunFilesList(&b, nil, now)
	if b.String() != "No run changed files.\n" {
		t.Errorf("empty list: %q", b.String())
	}
	b.Reset()
	printUndoSteps(&b, []runFilesStep{
		{Path: "notes.md", Op: "changed", Action: "restore", Done: true},
		{Path: "draft.md", Op: "created", Action: "trash", Done: true, To: ".fridolin-trash/20261119-120000/draft.md"},
		{Path: "summary.md", Op: "changed", Action: "conflict", Why: "it changed since the run"},
		{Path: "old.md", Op: "deleted", Action: "restore", Error: "permission denied"},
	})
	for _, want := range []string{"restore, done", "trash, done: .fridolin-trash/20261119-120000/draft.md", "conflict: it changed since the run", "restore, failed: permission denied"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("steps:\n%s\nlacks %q", b.String(), want)
		}
	}
}
