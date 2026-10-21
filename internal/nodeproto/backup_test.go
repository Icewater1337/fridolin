package nodeproto

import (
	"slices"
	"strings"
	"testing"
)

func TestPathWithin(t *testing.T) {
	for _, c := range []struct {
		base, p string
		want    bool
	}{
		{"/Volumes/Backup", "/Volumes/Backup", true},
		{"/Volumes/Backup", "/Volumes/Backup/fridolin", true},
		{"/Volumes/Backup/", "/Volumes/Backup/fridolin", true},
		{"/Volumes/Backup", "/Volumes/Backup2", false},
		{"/Volumes/Backup", "/Volumes/Backup/../Other", false},
		{"/", "/mnt/disk", true},
		{`D:\`, `D:\Backups\fridolin`, true},
		{`D:\`, `d:\backups`, true},
		{`D:\Backups`, `D:\Backups2`, false},
		{`D:\Backups`, `D:/Backups/x`, true},
		{`D:\Backups`, `E:\Backups`, false},
		{`\\nas\share`, `\\nas\share\fridolin`, true},
		{`\\nas\share`, `\\nas\sharex`, false},
		{"/mnt", `C:\mnt`, false},
	} {
		if got := PathWithin(c.base, c.p); got != c.want {
			t.Errorf("PathWithin(%q, %q) = %v, want %v", c.base, c.p, got, c.want)
		}
	}
	if !SamePath(`D:\Backups\`, `d:/backups`) || SamePath("/a/B", "/a/b") {
		t.Error("SamePath: Windows paths ignore case and separators, Unix ones do not")
	}
}

func TestCheckBackupFile(t *testing.T) {
	hex := strings.Repeat("ab", 32)
	for _, ok := range [][2]string{{"config", ""}, {"data", hex}, {"keys", hex}, {"locks", hex}, {"snapshots", hex}, {"index", hex}} {
		if err := CheckBackupFile(ok[0], ok[1]); err != nil {
			t.Errorf("%v: %v", ok, err)
		}
	}
	for _, bad := range [][2]string{{"config", hex}, {"data", ""}, {"data", "../x"}, {"data", strings.ToUpper(hex)}, {"other", hex}, {"keys", hex + "0"}, {"", ""}} {
		if err := CheckBackupFile(bad[0], bad[1]); err == nil {
			t.Errorf("%v: accepted", bad)
		}
	}
	if got := BackupRelPath("data", hex); got != "data/ab/"+hex {
		t.Errorf("data path %q", got)
	}
	if got := BackupRelPath("config", ""); got != "config" {
		t.Errorf("config path %q", got)
	}
}

func TestBackupApprovals(t *testing.T) {
	anywhere := Capabilities{Backup: &BackupCap{}}
	onD := Capabilities{Backup: &BackupCap{Roots: []string{`D:\`}}}
	approvedAnywhere := Capabilities{Backup: &BackupCap{Folders: []string{`D:\Backups\fridolin`, `C:\Users\you\b`}}}

	if Covers(Capabilities{}, anywhere) {
		t.Error("nothing approved covers a backup declaration")
	}
	if !Covers(approvedAnywhere, anywhere) || !Covers(approvedAnywhere, onD) {
		t.Error("an approval for every drive covers any declaration")
	}
	if Covers(onD, anywhere) {
		t.Error("a declaration that grows from D:\\ to every drive needs approval again")
	}
	if !Covers(onD, Capabilities{Backup: &BackupCap{Roots: []string{`d:\`}}}) {
		t.Error("the same root in another spelling is covered")
	}
	if Covers(onD, Capabilities{Backup: &BackupCap{Roots: []string{`D:\`, `E:\`}}}) {
		t.Error("a new root needs approval")
	}

	eff := Intersect(onD, approvedAnywhere)
	if eff.Backup == nil || !slices.Equal(eff.Backup.Folders, []string{`D:\Backups\fridolin`}) {
		t.Fatalf("a folder outside the declared roots is not served: %+v", eff.Backup)
	}
	if !eff.Allows(MethodBackupWrite) || !eff.Allows(MethodFSBrowse) || !eff.AllowsBackupFolder(`d:\backups\fridolin`) {
		t.Error("the approved folder is allowed")
	}
	if eff.AllowsBackupFolder(`C:\Users\you\b`) || eff.AllowsBackupFolder(`D:\Backups`) {
		t.Error("only the approved folder itself")
	}
	if !eff.AllowsBrowse(`D:\Backups`) || eff.AllowsBrowse(`C:\`) {
		t.Error("browse stays under the declared roots")
	}
	if Intersect(anywhere, Capabilities{}).Backup != nil || (Capabilities{}).Allows(MethodFSRoots) {
		t.Error("no approval, no backup")
	}
	if got := Intersect(Capabilities{Backup: &BackupCap{Roots: []string{"/a"}}}, Capabilities{Backup: &BackupCap{Roots: []string{"/b"}}}); got.Backup != nil {
		t.Errorf("roots that do not meet leave nothing: %+v", got.Backup)
	}
	if s := eff.Summary(); s != "backup (1 folder)" {
		t.Errorf("summary %q", s)
	}
	if (Capabilities{Backup: &BackupCap{}}).Empty() {
		t.Error("a backup declaration is not empty")
	}
	if err := (Capabilities{Backup: &BackupCap{Roots: []string{"relative"}}}).Check(); err == nil {
		t.Error("a relative root is refused")
	}
}
