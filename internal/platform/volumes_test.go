package platform

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeEntry struct {
	name string
	mode fs.FileMode
}

func (e fakeEntry) Name() string               { return e.name }
func (e fakeEntry) IsDir() bool                { return e.mode.IsDir() }
func (e fakeEntry) Type() fs.FileMode          { return e.mode.Type() }
func (e fakeEntry) Info() (fs.FileInfo, error) { return nil, errors.New("no info") }

type fakeInfo struct{ dir bool }

func (i fakeInfo) Name() string       { return "" }
func (i fakeInfo) Size() int64        { return 0 }
func (i fakeInfo) Mode() fs.FileMode  { return fs.ModeDir }
func (i fakeInfo) ModTime() time.Time { return time.Time{} }
func (i fakeInfo) IsDir() bool        { return i.dir }
func (i fakeInfo) Sys() any           { return nil }

func paths(vs []Volume) string {
	var out []string
	for _, v := range vs {
		s := v.Path
		if v.Removable {
			s += "*"
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

func TestVolumes(t *testing.T) {
	noDir := func(string) ([]os.DirEntry, error) { return nil, fs.ErrNotExist }
	stat := func(p string) (os.FileInfo, error) {
		if p == `C:\` || p == `D:\` {
			return fakeInfo{dir: true}, nil
		}
		return nil, fs.ErrNotExist
	}
	if got := paths(volumes("windows", `C:\Users\you`, nil, noDir, stat)); got != `C:\Users\you C:\ D:\*` {
		t.Errorf("windows: %s", got)
	}
	vols := func(string) ([]os.DirEntry, error) {
		return []os.DirEntry{fakeEntry{"Macintosh HD", fs.ModeSymlink}, fakeEntry{"Backup", fs.ModeDir}, fakeEntry{".timemachine", fs.ModeDir}}, nil
	}
	if got := paths(volumes("darwin", "/Users/you", nil, vols, stat)); got != "/Users/you / /Volumes/Backup*" {
		t.Errorf("darwin: %s", got)
	}
	mounts := strings.Join([]string{
		"/dev/sda1 / ext4 rw 0 0",
		"proc /proc proc rw 0 0",
		"/dev/sda2 /boot/efi vfat rw 0 0",
		"/dev/sdb1 /media/you/My\\040Disk exfat rw 0 0",
		"/dev/sdc1 /srv/data xfs rw 0 0",
		"tmpfs /run/user/1000 tmpfs rw 0 0",
		"/dev/sdd1 /run/media/you/usb vfat rw 0 0",
		"nas:/b /mnt/nas nfs4 rw 0 0",
	}, "\n")
	if got := paths(volumes("linux", "/home/you", strings.NewReader(mounts), noDir, stat)); got != "/home/you / /media/you/My Disk* /srv/data /run/media/you/usb* /mnt/nas*" {
		t.Errorf("linux: %s", got)
	}
}
