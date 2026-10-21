package sandbox

import (
	"fmt"
	"hash/fnv"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// BwrapPath is bubblewrap in the fridolin-core image (Debian's package).
const BwrapPath = "/usr/bin/bwrap"

// Brain users. Inside the container each brain runs as a Unix user of its
// own, brain-<id>, with its state folder as a 0700 home. The user exists
// in the run's user namespace: bubblewrap maps it onto the bridge's own
// user, so the bridge keeps owning every file a brain writes and no
// privilege is needed to start a run. What keeps brains apart is the
// mount namespace: a run sees the system read-only and only its own
// folders of the data volume; everything else of /data, other brains,
// bridge.db, secrets/, the config, does not exist for it. Its own PID
// namespace hides the bridge's processes (and their environment).
const (
	brainUIDBase  = 20000
	brainUIDRange = 40000
)

// BrainUser is the user name of brain id inside its sandbox.
func BrainUser(id string) string { return "brain-" + id }

// BrainUID is brain id's user and group id inside its sandbox: a stable
// number from 20000 to 59999 derived from the id. Two brains may share a
// number; nothing depends on them differing, since each run has a user
// namespace of its own.
func BrainUID(id string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return brainUIDBase + int(h.Sum32()%brainUIDRange)
}

// BrainPasswd and BrainGroup are the /etc/passwd and /etc/group a run of
// brain id sees: root and nobody, which own what is not the brain's, and
// its own user with home as its home. Programs ask for them (Node's
// os.userInfo() throws for a user it cannot find).
func BrainPasswd(id, home string) string {
	uid := strconv.Itoa(BrainUID(id))
	return "root:x:0:0:root:/root:/usr/sbin/nologin\n" +
		BrainUser(id) + ":x:" + uid + ":" + uid + ":fridolin brain " + id + ":" + home + ":/bin/sh\n" +
		"nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin\n"
}

// BrainGroup is the /etc/group of brain id's runs; see BrainPasswd.
func BrainGroup(id string) string {
	uid := strconv.Itoa(BrainUID(id))
	return "root:x:0:\n" + BrainUser(id) + ":x:" + uid + ":\nnogroup:x:65534:\n"
}

// BwrapPolicy is one brain's run under bubblewrap, with absolute paths.
type BwrapPolicy struct {
	Brain string
	// System are read-only binds of the image (/usr, /etc, the bridge's
	// install); a missing one is skipped.
	System []string
	// Write are the brain's own places, bound read-write: its folder (as
	// configured and its real path), its state (HOME), its media inbox.
	Write []string
	// Read are read-only binds besides the system: the run's token folder
	// and sandbox.allow_paths.
	Read []string
	// Mask are paths inside the binds above that the run must not see:
	// sandbox.deny_paths, other brains' folders, the database, the config
	// file and the credentials folder where a bind would show them. Paths
	// outside every bind are not in the sandbox anyway and are left out.
	Mask []string
	// Home is the brain's home, its state folder; Passwd and Group are the
	// files bound over /etc/passwd and /etc/group (BrainPasswd, BrainGroup).
	Home, Passwd, Group string
	// HostHome, when set, is the home folder of the machine the brain
	// folders come from: a native install moved into the container, whose
	// folders are bound at their own paths (docs/CORE.md). The run gets an
	// empty, private folder there as its HOME, mounted before the binds,
	// which then lie on top of it: a brain's folder at ~/Personal is
	// ~/Personal for the brain too, as its prompts and scripts say. What a
	// run writes there outside a bound folder ends with the run.
	HostHome string
	// Kind says what a path is: "dir", "file" or "" when it does not exist.
	Kind func(path string) string
}

// RunHome is the HOME of the policy's runs: HostHome when set, else the
// brain's state folder.
func (p BwrapPolicy) RunHome() string {
	if p.HostHome != "" {
		return filepath.Clean(p.HostHome)
	}
	return p.Home
}

// BwrapArgs returns bubblewrap's arguments that run prog with args as the
// policy's brain. The order matters: bubblewrap mounts in order, so the
// masks come after the binds they cover.
func BwrapArgs(p BwrapPolicy, prog string, args []string) ([]string, error) {
	if p.Brain == "" || p.Home == "" || p.Passwd == "" || p.Group == "" || p.Kind == nil {
		return nil, fmt.Errorf("bwrap policy for %q is incomplete", p.Brain)
	}
	paths := [][]string{p.System, p.Write, p.Read, p.Mask, {p.Home, p.Passwd, p.Group, prog}}
	if p.HostHome != "" {
		paths = append(paths, []string{p.HostHome})
	}
	for _, list := range paths {
		for _, path := range list {
			if !filepath.IsAbs(path) {
				return nil, fmt.Errorf("bwrap policy for %q: %q is not an absolute path", p.Brain, path)
			}
			if strings.ContainsRune(path, 0) {
				return nil, fmt.Errorf("bwrap policy for %q: a path holds NUL", p.Brain)
			}
		}
	}
	uid := strconv.Itoa(BrainUID(p.Brain))
	a := []string{
		// A user namespace with the brain's user, mapped onto the bridge's;
		// PID, IPC and cgroup namespaces of its own. The network stays the
		// container's: Claude needs it, and the brain API is on loopback.
		"--unshare-user", "--uid", uid, "--gid", uid,
		"--unshare-pid", "--unshare-ipc", "--unshare-cgroup-try",
		// The run ends with the bridge's supervision of it; no terminal
		// to inject keystrokes into; no capabilities.
		"--die-with-parent", "--new-session", "--cap-drop", "ALL",
	}
	for _, d := range clean(p.System) {
		a = append(a, "--ro-bind-try", d, d)
	}
	a = append(a,
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
		"--ro-bind", p.Passwd, "/etc/passwd", "--ro-bind", p.Group, "/etc/group",
	)
	home := p.RunHome()
	if p.HostHome != "" {
		a = append(a, "--tmpfs", home)
	}
	for _, d := range clean(p.Read) {
		a = append(a, "--ro-bind-try", d, d)
	}
	for _, d := range clean(p.Write) {
		a = append(a, "--bind", d, d)
	}
	visible := append(append(append([]string{}, p.System...), p.Read...), p.Write...)
	for _, m := range clean(p.Mask) {
		if !within(m, visible) {
			continue
		}
		switch p.Kind(m) {
		case "dir":
			// An empty, read-only folder over it.
			a = append(a, "--tmpfs", m, "--remount-ro", m)
		case "file":
			a = append(a, "--ro-bind", "/dev/null", m)
		}
	}
	a = append(a,
		"--setenv", "HOME", home,
		"--setenv", "USER", BrainUser(p.Brain), "--setenv", "LOGNAME", BrainUser(p.Brain),
		"--", prog)
	return append(a, args...), nil
}

// clean sorts and dedups absolute paths and leaves out those inside
// another one of them, which its bind shows already.
func clean(paths []string) []string {
	var out []string
	for _, p := range paths {
		if p != "" {
			out = append(out, filepath.Clean(p))
		}
	}
	slices.Sort(out)
	out = slices.Compact(out)
	var kept []string
	for _, p := range out {
		if !slices.ContainsFunc(out, func(d string) bool { return d != p && within(p, []string{d}) }) {
			kept = append(kept, p)
		}
	}
	return kept
}

func within(p string, dirs []string) bool {
	for _, d := range dirs {
		d = filepath.Clean(d)
		if p == d || strings.HasPrefix(p, strings.TrimSuffix(d, "/")+"/") {
			return true
		}
	}
	return false
}
