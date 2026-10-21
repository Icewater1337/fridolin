package platform

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Volume is a drive or volume of this machine a backup may go to.
type Volume struct {
	// Path is its root: C:\, /, /Volumes/Backup, /media/you/disk; the
	// home folder is one too.
	Path  string
	Label string
	// Removable is an external or network disk that may be away.
	Removable bool
}

// Volumes lists this machine's drives and volumes, the home folder
// first: Windows drive letters, macOS's /Volumes, and on Linux the
// mounts of disks (not of /proc, /sys and the like).
func Volumes() []Volume {
	home, _ := os.UserHomeDir()
	var mounts io.Reader
	if runtime.GOOS == "linux" {
		if f, err := os.Open("/proc/self/mounts"); err == nil {
			defer f.Close()
			mounts = f
		}
	}
	return volumes(runtime.GOOS, home, mounts, os.ReadDir, os.Stat)
}

// diskFS are the Linux file systems that hold files a person keeps.
func diskFS() []string {
	return []string{"ext2", "ext3", "ext4", "xfs", "btrfs", "zfs", "f2fs", "vfat", "exfat", "ntfs", "ntfs3", "fuseblk",
		"hfsplus", "apfs", "nfs", "nfs4", "cifs", "smb3", "fuse.sshfs", "fuse.rclone", "9p", "virtiofs"}
}

// systemMounts are Linux mount points that are the system's, not a disk
// for backups.
func systemMounts() []string {
	return []string{"/boot", "/proc", "/sys", "/dev", "/run", "/snap", "/var/lib", "/var/snap", "/etc", "/usr", "/tmp"}
}

func volumes(goos, home string, mounts io.Reader, readDir func(string) ([]os.DirEntry, error), stat func(string) (os.FileInfo, error)) []Volume {
	var out []Volume
	add := func(v Volume) {
		if v.Path != "" && !slices.ContainsFunc(out, func(o Volume) bool { return o.Path == v.Path }) {
			out = append(out, v)
		}
	}
	if home != "" {
		add(Volume{Path: home, Label: "Home"})
	}
	switch goos {
	case "windows":
		// A: and B: are floppy drives, which a look at may stall on.
		for c := 'C'; c <= 'Z'; c++ {
			p := string(c) + `:\`
			if st, err := stat(p); err == nil && st.IsDir() {
				add(Volume{Path: p, Label: string(c) + ":", Removable: c != 'C'})
			}
		}
	case "darwin":
		add(Volume{Path: "/", Label: "This Mac"})
		if ents, err := readDir("/Volumes"); err == nil {
			for _, e := range ents {
				// The startup disk is a link to /.
				if e.Type()&os.ModeSymlink != 0 || !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
					continue
				}
				add(Volume{Path: filepath.Join("/Volumes", e.Name()), Label: e.Name(), Removable: true})
			}
		}
	default:
		add(Volume{Path: "/", Label: "/"})
		if mounts == nil {
			break
		}
		sc := bufio.NewScanner(mounts)
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) < 3 || !slices.Contains(diskFS(), f[2]) {
				continue
			}
			p := unescapeMount(f[1])
			if p == "/" || slices.ContainsFunc(systemMounts(), func(s string) bool { return p == s || strings.HasPrefix(p, s+"/") }) &&
				!strings.HasPrefix(p, "/run/media/") {
				continue
			}
			removable := strings.HasPrefix(p, "/media/") || strings.HasPrefix(p, "/run/media/") || strings.HasPrefix(p, "/mnt/") ||
				strings.HasPrefix(f[2], "nfs") || f[2] == "cifs" || f[2] == "smb3"
			add(Volume{Path: p, Label: filepath.Base(p), Removable: removable})
		}
	}
	return out
}

// unescapeMount undoes /proc/self/mounts' octal escapes (\040 for a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				sb.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}
