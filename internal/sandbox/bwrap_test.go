package sandbox

import (
	"slices"
	"strings"
	"testing"
)

// In the container brain folders lie in the data dir's brains-folders,
// which its protection leaves to them, and nowhere else in it.
func TestBrainsRootInTheDataDir(t *testing.T) {
	s := Settings{DataDir: "/data", BrainsRoot: "/data/brains-folders"}
	ctx := Context{Home: "/var/lib/fridolin", WorkingDir: "/"}
	protected := s.Protected(ctx, InstallLayout{}, Programs{})
	for dir, refused := range map[string]bool{
		"/data/brains-folders/a":         false,
		"/data/brains-folders/a/b":       false,
		"/data/brains-folders":           true,
		"/data/secrets":                  true,
		"/data/brains-folders-elsewhere": true,
		"/data":                          true,
	} {
		if what, got := ProtectedIn(ctx, dir, protected); got != refused {
			t.Errorf("%s: refused %v (%s), want %v", dir, got, what, refused)
		}
	}
	s.BrainsRoot = ""
	if _, got := ProtectedIn(ctx, "/data/brains-folders/a", s.Protected(ctx, InstallLayout{}, Programs{})); !got {
		t.Error("outside the container a brain folder in the data dir was allowed")
	}
}

func TestBwrapArgs(t *testing.T) {
	p := BwrapPolicy{Brain: "a", Home: "/data/brains/a", Passwd: "/data/core/sandbox/a/passwd", Group: "/data/core/sandbox/a/group",
		System: []string{"/usr", "/usr/local/bin", "/etc"}, Write: []string{"/data/brains-folders/a"},
		Mask: []string{"/data/brains-folders/a/x", "/srv/not-bound"}, Kind: func(string) string { return "dir" }}
	args, err := BwrapArgs(p, "/usr/local/bin/claude", []string{"-p"})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(args, " ")
	for _, want := range []string{"--ro-bind-try /etc /etc --ro-bind-try /usr /usr", "--tmpfs /data/brains-folders/a/x --remount-ro /data/brains-folders/a/x", "-- /usr/local/bin/claude -p"} {
		if !strings.Contains(line, want) {
			t.Errorf("no %q in %s", want, line)
		}
	}
	if strings.Contains(line, "/usr/local/bin /usr/local/bin") || strings.Contains(line, "/srv/not-bound") {
		t.Errorf("a nested bind or a mask of what is not bound: %s", line)
	}
	if i := slices.Index(args, "--uid"); i < 0 || args[i+1] != args[slices.Index(args, "--gid")+1] {
		t.Errorf("uid and gid: %s", line)
	}
	p.Write = []string{"relative"}
	if _, err := BwrapArgs(p, "/x", nil); err == nil {
		t.Error("a relative path was taken")
	}
	if !strings.Contains(BrainPasswd("a", "/data/brains/a"), "brain-a:x:") || !strings.Contains(BrainGroup("a"), "brain-a:x:") {
		t.Error("passwd or group lacks the brain's user")
	}
}
