package nodeagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
)

func code(err error) string {
	if pe, ok := errors.AsType[*nodeproto.Error](err); ok {
		return pe.Code
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

// approvedBackupNode joins a node that offers backups with caps, and
// approves its device and what it offers.
func approvedBackupNode(t *testing.T, caps nodeproto.Capabilities, vols []platform.Volume) (*core, *Agent) {
	t.Helper()
	c := startCore(t, t.TempDir())
	a := joinNode(t, c, caps)
	a.Volumes = func() []platform.Volume { return vols }
	runAgent(t, a)
	waitFor(t, "connected", a.Connected)
	if _, err := c.svc.ApproveDevice(a.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.svc.ApproveCommands(a.ID()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "backup approved", func() bool { return a.State().Approved.Backup != nil })
	return c, a
}

// TestBrowseFakeRoots: fs.roots lists the drives the OS reports (here, fake
// ones), fs.browse lists folder names only, and both need backup approved.
func TestBrowseFakeRoots(t *testing.T) {
	disk, home := t.TempDir(), t.TempDir()
	for _, d := range []string{"Backups", "Photos", ".hidden", "Backups/fridolin"} {
		if err := os.MkdirAll(filepath.Join(disk, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(disk, "secret.txt"), []byte("never listed"), 0o600); err != nil {
		t.Fatal(err)
	}
	vols := []platform.Volume{{Path: home, Label: "Home"}, {Path: disk, Label: "Backup", Removable: true}, {Path: filepath.Join(disk, "gone"), Label: "Unplugged"}}
	c, a := approvedBackupNode(t, nodeproto.Capabilities{Backup: &nodeproto.BackupCap{}}, vols)
	ctx := context.Background()

	roots, err := c.svc.Roots(ctx, a.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(roots.Roots) != 2 || roots.Roots[1].Path != disk || !roots.Roots[1].Removable || roots.Sep != string(filepath.Separator) {
		t.Fatalf("roots: %+v (an unplugged disk is left out)", roots)
	}
	got, err := c.svc.Browse(ctx, a.ID(), nodeproto.BrowseParams{Path: disk})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Folders, []string{"Backups", "Photos"}) || got.Repo {
		t.Fatalf("browse: %+v (names of folders only, no file, nothing hidden)", got)
	}
	if got, _ = c.svc.Browse(ctx, a.ID(), nodeproto.BrowseParams{Path: disk, Hidden: true}); !slices.Contains(got.Folders, ".hidden") {
		t.Fatalf("hidden: %+v", got)
	}
	if _, err := c.svc.Browse(ctx, a.ID(), nodeproto.BrowseParams{Path: "relative"}); code(err) != nodeproto.CodeBadRequest {
		t.Fatalf("a relative path: %v", err)
	}
	if _, err := c.svc.Browse(ctx, a.ID(), nodeproto.BrowseParams{Path: filepath.Join(disk, "secret.txt")}); err == nil {
		t.Fatal("browsing a file lists nothing")
	}
}

// TestBrowseStaysInDeclaredRoots: a node that lends one drive lets the
// picker see that drive only.
func TestBrowseStaysInDeclaredRoots(t *testing.T) {
	disk, other := t.TempDir(), t.TempDir()
	c, a := approvedBackupNode(t, nodeproto.Capabilities{Backup: &nodeproto.BackupCap{Roots: []string{disk}}}, []platform.Volume{{Path: other}})
	ctx := context.Background()
	roots, err := c.svc.Roots(ctx, a.ID())
	if err != nil || len(roots.Roots) != 1 || roots.Roots[0].Path != disk {
		t.Fatalf("roots: %+v %v", roots, err)
	}
	if _, err := c.svc.Browse(ctx, a.ID(), nodeproto.BrowseParams{Path: other}); code(err) != nodeproto.CodeOutsideRoot {
		t.Fatalf("browsing outside the lent drive: %v", err)
	}
	if got, err := c.svc.Browse(ctx, a.ID(), nodeproto.BrowseParams{Path: disk}); err != nil || got.Parent != "" {
		t.Fatalf("the lent root has no parent to go up to: %+v %v", got, err)
	}
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TestBackupRepoFiles: the repository's files go only into an approved
// folder, by restic's names, in chunks checked by their hash.
func TestBackupRepoFiles(t *testing.T) {
	disk := t.TempDir()
	folder := filepath.Join(disk, "fridolin")
	c, a := approvedBackupNode(t, nodeproto.Capabilities{Backup: &nodeproto.BackupCap{Roots: []string{disk}}}, nil)
	ctx := context.Background()
	call := func(method string, params, out any) error { return c.svc.Call(ctx, a.ID(), method, params, out) }

	if err := call(nodeproto.MethodBackupCreate, nodeproto.BackupFolderParams{Folder: folder}, nil); code(err) != nodeproto.CodeDenied {
		t.Fatalf("a folder the owner did not pick: %v", err)
	}
	if _, err := c.svc.ApproveBackupFolder(a.ID(), folder); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the folder approved", func() bool { return a.State().Approved.AllowsBackupFolder(folder) })
	var info nodeproto.BackupInfo
	if err := call(nodeproto.MethodBackupCreate, nodeproto.BackupFolderParams{Folder: folder}, &info); err != nil || !info.Exists || info.Repo {
		t.Fatalf("create: %+v %v", info, err)
	}

	data := bytes.Repeat([]byte("restic pack "), 100000)
	name := sum(data)
	file := nodeproto.BackupFileParams{Folder: folder, Type: nodeproto.BackupData, Name: name}
	upload := strings.Repeat("ab", 8)
	half := len(data) / 2
	if err := call(nodeproto.MethodBackupWrite, nodeproto.BackupWriteParams{BackupFileParams: file, Upload: upload, Data: data[:half]}, nil); err != nil {
		t.Fatal(err)
	}
	if err := call(nodeproto.MethodBackupStat, file, nil); code(err) != nodeproto.CodeNotFound {
		t.Fatalf("a file appears only once its last chunk is in: %v", err)
	}
	if err := call(nodeproto.MethodBackupWrite, nodeproto.BackupWriteParams{BackupFileParams: file, Upload: upload, Offset: 7, Data: data[half:]}, nil); code(err) != nodeproto.CodeBadRequest {
		t.Fatalf("a chunk out of order: %v", err)
	}
	// The upload was dropped; start over, and finish with a wrong hash.
	_ = call(nodeproto.MethodBackupWrite, nodeproto.BackupWriteParams{BackupFileParams: file, Upload: upload, Data: data[:half]}, nil)
	if err := call(nodeproto.MethodBackupWrite, nodeproto.BackupWriteParams{BackupFileParams: file, Upload: upload, Offset: int64(half), Data: data[half:],
		Final: true, Size: int64(len(data)), SHA256: sum(nil)}, nil); code(err) != nodeproto.CodeFailed {
		t.Fatalf("a wrong hash: %v", err)
	}
	_ = call(nodeproto.MethodBackupWrite, nodeproto.BackupWriteParams{BackupFileParams: file, Upload: upload, Data: data[:half]}, nil)
	if err := call(nodeproto.MethodBackupWrite, nodeproto.BackupWriteParams{BackupFileParams: file, Upload: upload, Offset: int64(half), Data: data[half:],
		Final: true, Size: int64(len(data)), SHA256: name}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(folder, "data", name[:2], name)); err != nil {
		t.Fatalf("restic's local layout: %v", err)
	}
	var st nodeproto.BackupFile
	if err := call(nodeproto.MethodBackupStat, file, &st); err != nil || st.Size != int64(len(data)) {
		t.Fatalf("stat: %+v %v", st, err)
	}
	var list nodeproto.BackupListResult
	if err := call(nodeproto.MethodBackupList, nodeproto.BackupFileParams{Folder: folder, Type: nodeproto.BackupData}, &list); err != nil || len(list.Files) != 1 || list.Files[0].Name != name {
		t.Fatalf("list: %+v %v", list, err)
	}
	var rd nodeproto.BackupReadResult
	if err := call(nodeproto.MethodBackupRead, nodeproto.BackupReadParams{BackupFileParams: file, Offset: 12, Length: 6}, &rd); err != nil || string(rd.Data) != "restic" || rd.Size != int64(len(data)) {
		t.Fatalf("read: %q %v", rd.Data, err)
	}

	// The same file again with other content is refused; config is never
	// deleted; nothing but restic's names.
	other := []byte("other")
	if err := call(nodeproto.MethodBackupWrite, nodeproto.BackupWriteParams{BackupFileParams: file, Upload: strings.Repeat("cd", 8), Data: other,
		Final: true, Size: 5, SHA256: sum(other)}, nil); code(err) != nodeproto.CodeDenied {
		t.Fatalf("replacing a file: %v", err)
	}
	cfg := []byte("config")
	cfgFile := nodeproto.BackupFileParams{Folder: folder, Type: nodeproto.BackupConfig}
	if err := call(nodeproto.MethodBackupWrite, nodeproto.BackupWriteParams{BackupFileParams: cfgFile, Upload: strings.Repeat("ef", 8), Data: cfg, Final: true, Size: 6, SHA256: sum(cfg)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := call(nodeproto.MethodBackupDelete, cfgFile, nil); code(err) != nodeproto.CodeDenied {
		t.Fatalf("deleting config: %v", err)
	}
	for _, bad := range []nodeproto.BackupFileParams{
		{Folder: folder, Type: "data", Name: "../../escape"},
		{Folder: folder, Type: "secrets", Name: name},
		{Folder: disk, Type: "data", Name: name},
	} {
		if err := call(nodeproto.MethodBackupStat, bad, nil); err == nil {
			t.Errorf("%+v: allowed", bad)
		}
	}
	if err := call(nodeproto.MethodBackupDelete, file, nil); err != nil {
		t.Fatal(err)
	}
	if err := call(nodeproto.MethodBackupStat, file, nil); code(err) != nodeproto.CodeNotFound {
		t.Fatalf("deleted: %v", err)
	}

	// Taking the folder away stops it at once, on the node too.
	if _, err := c.svc.RemoveBackupFolder(a.ID(), folder); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the folder withdrawn", func() bool { return !a.State().Approved.AllowsBackupFolder(folder) })
	if _, perr := a.backupCall(a.State().Approved, nodeproto.MethodBackupInfo, []byte(`{"folder":`+jsonQuote(folder)+`}`)); perr == nil || perr.Code != nodeproto.CodeDenied {
		t.Fatalf("the node refuses a withdrawn folder itself: %v", perr)
	}
}

func jsonQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestConfigRefusesBackupFolders(t *testing.T) {
	cfg := Config{Core: "core:8766", Name: "n", Capabilities: nodeproto.Capabilities{Backup: &nodeproto.BackupCap{Folders: []string{t.TempDir()}}}}
	if err := cfg.Check(); err == nil {
		t.Fatal("node.json may not name backup folders: the owner picks them on the core")
	}
}
