package platform

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// clockTicks is USER_HZ, the unit of the times in /proc/<pid>/stat: 100
// on every Linux the bridge runs on (sysconf(_SC_CLK_TCK), which Go
// cannot ask without cgo).
const clockTicks = 100

// processes reads /proc.
func processes() ([]Proc, error) {
	boot, err := bootTime()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	page := uint64(os.Getpagesize())
	var out []Proc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		p, err := readProc(pid, boot, page)
		if err != nil {
			continue // ended in between, or unreadable
		}
		out = append(out, p)
	}
	return out, nil
}

// bootTime is when the machine booted, from /proc/stat's btime.
func bootTime() (time.Time, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			sec, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(sec, 0), nil
		}
	}
	return time.Time{}, fmt.Errorf("/proc/stat: no btime")
}

func readProc(pid int, boot time.Time, page uint64) (Proc, error) {
	dir := "/proc/" + strconv.Itoa(pid)
	data, err := os.ReadFile(dir + "/stat")
	if err != nil {
		return Proc{}, err
	}
	// The command name in parentheses may hold anything, spaces and ")"
	// included; the fields after its last ")" start with the state.
	open, end := bytes.IndexByte(data, '('), bytes.LastIndexByte(data, ')')
	if open < 0 || end < open {
		return Proc{}, fmt.Errorf("%s/stat: no command name", dir)
	}
	f := strings.Fields(string(data[end+1:]))
	if len(f) < 22 {
		return Proc{}, fmt.Errorf("%s/stat: too few fields", dir)
	}
	num := func(i int) int64 { n, _ := strconv.ParseInt(f[i], 10, 64); return n }
	// f[0] is field 3 (state); field n is f[n-3].
	p := Proc{
		PID: pid, PPID: int(num(1)), PGID: int(num(2)), SID: int(num(3)), UID: -1,
		Name:   string(data[open+1 : end]),
		Zombie: f[0] == "Z",
		CPU:    time.Duration(num(11)+num(12)) * time.Second / clockTicks,
		Start:  boot.Add(time.Duration(num(19)) * time.Second / clockTicks),
		RSS:    uint64(max(num(21), 0)) * page,
	}
	if fi, err := os.Stat(dir); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			p.UID = int(st.Uid)
		}
	}
	if cmdline, err := os.ReadFile(dir + "/cmdline"); err == nil && len(cmdline) > 0 {
		p.Args = splitNUL(cmdline)
	}
	return p, nil
}

// splitNUL splits NUL-terminated strings.
func splitNUL(b []byte) []string {
	b = bytes.TrimRight(b, "\x00")
	if len(b) == 0 {
		return nil
	}
	parts := bytes.Split(b, []byte{0})
	out := make([]string, len(parts))
	for i, s := range parts {
		out[i] = string(s)
	}
	return out
}

func processEnv(pid int) ([]string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return nil, err
	}
	return splitNUL(data), nil
}
