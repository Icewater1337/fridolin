package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Nothing else tells a group chat by the end of its JID, so no check can
// drift from isGroupJID's, which is whatsmeow's types.GroupServer. It reads
// every Go file of the module, not just this package's, so the check still
// covers them all once the code is split into packages.
func TestGroupJIDsAreToldApartInOnePlace(t *testing.T) {
	bySuffix := regexp.MustCompile(`HasSuffix\(.*("@g\.us"|types\.GroupServer)`)
	root := repoDir
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		if filepath.Dir(root) == root {
			t.Fatalf("no go.mod above %s", repoDir)
		}
		root = filepath.Dir(root)
	}
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || d.Name() == "node_modules"):
			return filepath.SkipDir
		case !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go"):
			files = append(files, path)
		}
		return nil
	})
	if err != nil || len(files) == 0 {
		t.Fatalf("no Go files in %s: %v", root, err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if bySuffix.MatchString(line) && !strings.HasPrefix(line, "func IsGroupJID(") {
				rel, _ := filepath.Rel(root, f)
				t.Errorf("%s:%d tells a group chat by its suffix; call isGroupJID: %s", rel, i+1, strings.TrimSpace(line))
			}
		}
	}
}
