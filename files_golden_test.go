package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// tree lists the files below dir for a golden file: each one's path, mode
// and SHA-256, and the whole text of those named in show.
func tree(t *testing.T, s *scrubber, dir string, show ...string) []byte {
	t.Helper()
	var sb strings.Builder
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			fmt.Fprintf(&sb, "%s/ %s\n", filepath.ToSlash(rel), info.Mode().Perm())
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(s.text(string(data))))
		fmt.Fprintf(&sb, "%s %s %s\n", filepath.ToSlash(rel), info.Mode().Perm(), hex.EncodeToString(sum[:8]))
		if slices.Contains(show, filepath.ToSlash(rel)) {
			// Indented, and without trailing blanks, which editors strip;
			// the checksum covers the bytes.
			for _, l := range strings.Split(strings.TrimSuffix(s.text(string(data)), "\n"), "\n") {
				sb.WriteString(strings.TrimRight("\t"+l, " \t") + "\n")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return []byte(sb.String())
}

// TestGoldenBackupFiles pins what a backup leaves behind and asks of
// rclone: a brain's filter file made from its excludes, the folder the
// bridge's own configuration is staged in, and the rclone commands of one
// run, a brain and the bridge, then the prune.
func TestGoldenBackupFiles(t *testing.T) {
	h, s, fakes := goldenBridge(t)
	backup := filepath.Join(h.DataDir(), "backup")
	filter, err := os.ReadFile(filepath.Join(backup, "filters-main.txt"))
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "backup/filters-main.txt", filter)
	assertGolden(t, "backup/staging.golden", tree(t, s, filepath.Join(backup, "_staging"), "config.json", "README.txt", "brains/main.brain.json"))

	calls, err := os.ReadFile(filepath.Join(fakes, "calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		// The Backup page asks for the version while the run goes on.
		if l != "version" {
			lines = append(lines, s.text(l))
		}
	}
	assertGolden(t, "backup/rclone-calls.golden", []byte(strings.Join(lines, "\n")+"\n"))
}

// TestGoldenScaffold pins the folder Add brain makes for a new brain: the
// files and their modes, brain.json and the starter prompt in full, and the
// examples' skills by checksum.
func TestGoldenScaffold(t *testing.T) {
	h := newHarness(t, "")
	dir := h.AddBrain(BrainManifest{ID: "notes", Name: "Notes", Routing: BrainRouting{Groups: []string{harnessGroup}}})
	assertGolden(t, "scaffold/tree.golden", tree(t, h.scrubber(), dir, ManifestFile, defaultPromptFile, identityMapFile))
}
