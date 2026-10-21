package nodes

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// TestBackupApprovalFlow: backup is declared by the node and approved like
// exec and fs, a declaration that grows waits for approval again, and the
// folders of the targets are the owner's, kept across approvals.
func TestBackupApprovalFlow(t *testing.T) {
	clk := &clock{time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	r, err := OpenRegistry(t.TempDir(), clk.now)
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := r.NewJoinCode()
	if err != nil {
		t.Fatal(err)
	}
	h := hello(t, code, nodeproto.Capabilities{Host: true, Backup: &nodeproto.BackupCap{Roots: []string{`D:\`}}})
	n, err := r.Hello(h, "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ApproveBackupFolder(n.ID, `D:\Backups\fridolin`); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("a folder before the device is approved: %v", err)
	}
	if _, err := r.ApproveDevice(n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ApproveBackupFolder(n.ID, `D:\Backups\fridolin`); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a folder before backup is approved: %v", err)
	}
	if n, err = r.ApproveCommands(n.ID); err != nil || n.Commands() != nodeproto.CommandsApproved {
		t.Fatalf("approve commands: %v %+v", err, n)
	}
	if _, err := r.ApproveBackupFolder(n.ID, `C:\Users\you`); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a folder outside the lent drive: %v", err)
	}
	if _, err := r.ApproveBackupFolder(n.ID, "relative"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a relative folder: %v", err)
	}
	n, err = r.ApproveBackupFolder(n.ID, `D:\Backups\fridolin`)
	if err != nil {
		t.Fatal(err)
	}
	if !n.Effective().AllowsBackupFolder(`D:\Backups\fridolin`) || n.Commands() != nodeproto.CommandsApproved {
		t.Fatalf("the folder is approved and nothing else changed: %+v", n.Effective().Backup)
	}
	if n, _ = r.ApproveBackupFolder(n.ID, `d:\backups\fridolin`); len(n.Approved.Backup.Folders) != 1 {
		t.Fatalf("the same folder twice: %v", n.Approved.Backup.Folders)
	}

	// The node lends a second drive: that waits for approval, and the node
	// keeps serving the first meanwhile.
	h.JoinCode = ""
	h.Capabilities.Backup = &nodeproto.BackupCap{Roots: []string{`D:\`, `E:\`}}
	if n, err = r.Hello(h, "a"); err != nil {
		t.Fatal(err)
	}
	if n.Commands() != nodeproto.CommandsPending {
		t.Fatalf("a grown backup declaration: commands %s", n.Commands())
	}
	if !n.Effective().AllowsBackupFolder(`D:\Backups\fridolin`) {
		t.Fatal("the approved folder is still served while the rest waits")
	}
	if n, err = r.ApproveCommands(n.ID); err != nil || n.Commands() != nodeproto.CommandsApproved {
		t.Fatalf("approve again: %v", err)
	}
	if !slices.Equal(n.Approved.Backup.Folders, []string{`D:\Backups\fridolin`}) {
		t.Fatalf("approving again keeps the folders: %+v", n.Approved.Backup)
	}

	// Removing the target takes the folder away; revoking takes it all.
	if n, err = r.RemoveBackupFolder(n.ID, `D:\Backups\fridolin`); err != nil || n.Effective().AllowsBackupFolder(`D:\Backups\fridolin`) {
		t.Fatalf("remove: %v %+v", err, n.Approved.Backup)
	}
	if _, err := r.ApproveBackupFolder(n.ID, `E:\b`); err != nil {
		t.Fatal(err)
	}
	if n, err = r.Revoke(n.ID); err != nil || n.Approved.Backup != nil || n.Effective().Allows(nodeproto.MethodBackupWrite) {
		t.Fatalf("revoke: %v %+v", err, n)
	}
}
