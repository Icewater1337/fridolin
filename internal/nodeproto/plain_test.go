package nodeproto

import (
	"strings"
	"testing"
)

// Plain says one sentence per family offered, in plain words, and none
// for what is not.
func TestPlain(t *testing.T) {
	if got := (Capabilities{}).Plain(); len(got) != 0 {
		t.Errorf("nothing offered: %q", got)
	}
	c := Capabilities{
		Exec: &ExecCap{Allow: []ExecRule{{Name: "git"}}}, FS: &FSCap{Roots: []FSRoot{{Path: "/a"}, {Path: "/b", Write: true}}},
		Host: true, Notify: true, Fride: &FrideCap{}, AndroidBuild: &AndroidBuildCap{}, Backup: &BackupCap{}, Update: &UpdateCap{},
		Agent: &WorkstationCap{Roots: []string{"/w"}}, Terminal: &WorkstationCap{Roots: []string{"/1", "/2", "/3", "/4"}}, CoreHost: &CoreHostCap{},
	}
	got := strings.Join(c.Plain(), "\n")
	want := strings.Join([]string{
		"run these programs there when asked: git",
		"read the files in /a",
		"read and change the files in /b",
		"report its system details (OS, disk, memory)",
		"show notifications on its screen",
		"run FrIDE coding tasks on this machine — that means it can run any command there",
		"build Android apps there",
		"keep Fridolin's encrypted backups in folders there",
		"update Fridolin there to the core's version",
		"run Claude Code or Codex sessions in /w — they can run any command there",
		"open a terminal in /1, /2 and 2 more folders — anything you could type there",
		CoreHostPlain,
	}, "\n")
	if got != want {
		t.Errorf("Plain:\n%s\nwant:\n%s", got, want)
	}
}
