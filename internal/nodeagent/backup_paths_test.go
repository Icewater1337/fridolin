package nodeagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// call runs one backup method on the node side with the approvals eff,
// as the core's request would.
func call(t *testing.T, a *Agent, eff nodeproto.Capabilities, method string, params any) (any, *nodeproto.Error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return a.backupCall(eff, method, raw)
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
}

// untouched fails the test when anything was written into dir.
func untouched(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) > 0 {
		t.Fatalf("%s was written to: %v", dir, ents)
	}
}

// TestBackupPathTraversal: whatever a request names, the node writes and
// reads only restic's files in the approved folder, under the drive it
// lends: no .., no absolute paths, no names outside restic's layout, no
// symlink that leads out, no look-alike spelling.
func TestBackupPathTraversal(t *testing.T) {
	disk, outside := t.TempDir(), t.TempDir()
	folder := filepath.Join(disk, "fridolin")
	eff := nodeproto.Capabilities{Backup: &nodeproto.BackupCap{Roots: []string{disk}, Folders: []string{folder}}}
	a := &Agent{Logf: t.Logf}
	if _, perr := call(t, a, eff, nodeproto.MethodBackupCreate, nodeproto.BackupFolderParams{Folder: folder}); perr != nil {
		t.Fatal(perr)
	}
	hexName := strings.Repeat("ab", 32)
	data := []byte("restic pack")
	sum := sha256.Sum256(data)
	write := func(folder, typ, name, upload string) *nodeproto.Error {
		_, perr := call(t, a, eff, nodeproto.MethodBackupWrite, nodeproto.BackupWriteParams{
			BackupFileParams: nodeproto.BackupFileParams{Folder: folder, Type: typ, Name: name},
			Upload:           upload, Data: data, Final: true, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
		return perr
	}
	upload := strings.Repeat("cd", 8)

	for name, c := range map[string]struct{ folder, typ, name, upload string }{
		"folder with ..":           {folder + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(outside), "data", hexName, upload},
		"folder ../ back in":       {filepath.Join(folder, "..", "fridolin", "..", ".."), "data", hexName, upload},
		"relative folder":          {"fridolin", "data", hexName, upload},
		"another absolute folder":  {outside, "data", hexName, upload},
		"name with ..":             {folder, "data", "../../" + hexName[:58], upload},
		"absolute name":            {folder, "data", filepath.Join(outside, "x"), upload},
		"name too short":           {folder, "data", hexName[:62], upload},
		"upper-case hex":           {folder, "keys", strings.ToUpper(hexName), upload},
		"full-width digits":        {folder, "keys", strings.Repeat("ａｂ", 32), upload},
		"arabic-indic digits":      {folder, "keys", strings.Repeat("٠١", 32), upload},
		"NUL in name":              {folder, "keys", hexName[:63] + "\x00", upload},
		"type with a path":         {folder, "data/../keys", hexName, upload},
		"type outside the layout":  {folder, "secrets", hexName, upload},
		"config with a name":       {folder, "config", hexName, upload},
		"upload with ..":           {folder, "keys", hexName, "../../" + upload},
		"upload absolute":          {folder, "keys", hexName, filepath.Join(outside, upload)},
		"upload not hex":           {folder, "keys", hexName, "ghijklmnopqrstuv"},
		"folder in other spelling": {folder + "​", "keys", hexName, upload},
	} {
		if perr := write(c.folder, c.typ, c.name, c.upload); perr == nil {
			t.Errorf("%s: written", name)
		}
	}
	untouched(t, outside)

	// A folder inside the repository that is a symlink out of it: os.Root
	// refuses to follow it, for a write and for a read.
	if err := os.RemoveAll(filepath.Join(folder, "data", "ab")); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, filepath.Join(folder, "data", "ab"))
	if perr := write(folder, "data", hexName, upload); perr == nil {
		t.Error("a write through a symlinked subfolder")
	}
	if err := os.WriteFile(filepath.Join(outside, hexName), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, perr := call(t, a, eff, nodeproto.MethodBackupRead, nodeproto.BackupReadParams{BackupFileParams: nodeproto.BackupFileParams{Folder: folder, Type: "data", Name: hexName}}); perr == nil {
		t.Error("a read through a symlinked subfolder")
	}
	if _, perr := call(t, a, eff, nodeproto.MethodBackupDelete, nodeproto.BackupFileParams{Folder: folder, Type: "data", Name: hexName}); perr == nil {
		t.Error("a delete through a symlinked subfolder")
	}
	if _, err := os.Stat(filepath.Join(outside, hexName)); err != nil {
		t.Fatal("the file outside was touched")
	}
	if err := os.Remove(filepath.Join(outside, hexName)); err != nil {
		t.Fatal(err)
	}

	// An approved folder that is itself a symlink out of the lent drive.
	linked := filepath.Join(disk, "linked")
	symlinkOrSkip(t, outside, linked)
	effLinked := nodeproto.Capabilities{Backup: &nodeproto.BackupCap{Roots: []string{disk}, Folders: []string{linked, filepath.Join(linked, "deeper")}}}
	for _, f := range []string{linked, filepath.Join(linked, "deeper")} {
		if _, perr := call(t, a, effLinked, nodeproto.MethodBackupCreate, nodeproto.BackupFolderParams{Folder: f}); perr == nil || perr.Code != nodeproto.CodeOutsideRoot {
			t.Errorf("create in %s: %v", f, perr)
		}
	}
	untouched(t, outside)

	// Browsing: a symlink out of the lent drive lists nothing.
	if _, perr := call(t, a, eff, nodeproto.MethodFSBrowse, nodeproto.BrowseParams{Path: linked}); perr == nil || perr.Code != nodeproto.CodeOutsideRoot {
		t.Errorf("browse through a symlink: %v", perr)
	}
	for _, p := range []string{outside, "relative", filepath.Join(disk, "..")} {
		if _, perr := call(t, a, eff, nodeproto.MethodFSBrowse, nodeproto.BrowseParams{Path: p}); perr == nil {
			t.Errorf("browse %q", p)
		}
	}
}

func TestFileRelIsRestic(t *testing.T) {
	id := strings.Repeat("0f", 32)
	for typ, want := range map[string]string{
		"config":    "config",
		"data":      filepath.Join("data", "0f", id),
		"keys":      filepath.Join("keys", id),
		"locks":     filepath.Join("locks", id),
		"snapshots": filepath.Join("snapshots", id),
		"index":     filepath.Join("index", id),
	} {
		name := id
		if typ == "config" {
			name = ""
		}
		got, err := fileRel(typ, name)
		if err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", typ, got, err, want)
		}
	}
	for _, bad := range [][2]string{{"data", ""}, {"config", id}, {"..", id}, {"data", id + "/"}} {
		if _, err := fileRel(bad[0], bad[1]); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	base := filepath.Join(t.TempDir(), "b")
	if !inside(base, filepath.Join(base, "c")) || inside(base, base+"c") || inside(base, filepath.Dir(base)) || inside("a", filepath.Join("a", "b")) {
		t.Error("inside")
	}
}
