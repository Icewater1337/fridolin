package platform

import (
	"errors"
	"path/filepath"
	"strings"
	"time"
)

// Proc is one process as the system lists it, for housekeeping: who it
// is, who started it, and what it holds.
type Proc struct {
	PID  int `json:"pid"`
	PPID int `json:"ppid"`
	// PGID and SID are its process group and session; 0 where the system
	// has none (Windows).
	PGID int `json:"pgid,omitempty"`
	SID  int `json:"sid,omitempty"`
	// UID is the user it runs as, -1 when the system does not say.
	UID int `json:"uid"`
	// Name is the command name the system keeps (the executable's base
	// name, possibly cut short); Args its command line, nil when it cannot
	// be read (another user's process, or Windows).
	Name string   `json:"name"`
	Args []string `json:"args,omitempty"`
	// Start is when it started: what tells it from a later process given
	// the same PID. Zero when the system does not say.
	Start time.Time `json:"start,omitzero"`
	// RSS is its resident memory in bytes and CPU the processor time it
	// has used, user and system; 0 when they cannot be read.
	RSS uint64        `json:"rss"`
	CPU time.Duration `json:"cpu"`
	// Zombie: it has ended and waits for its parent to collect its status.
	Zombie bool `json:"zombie,omitempty"`
}

// Base is the program the process runs, by its base name: the first
// argument's when there is one, else the command name.
func (p Proc) Base() string {
	if len(p.Args) > 0 && p.Args[0] != "" {
		return filepath.Base(strings.ReplaceAll(p.Args[0], `\`, "/"))
	}
	return p.Name
}

// Processes lists the processes of this machine (of this container,
// inside one). Fields a process does not let this user read stay empty.
func Processes() ([]Proc, error) { return processes() }

// ProcessEnv reads the environment process pid started with, as
// NAME=value strings. Only this user's processes can be read.
func ProcessEnv(pid int) ([]string, error) { return processEnv(pid) }

// ProcessAlive reports whether a process pid exists (a zombie counts).
func ProcessAlive(pid int) bool { return pid > 0 && processAlive(pid) }

// SignalProcess asks process pid to stop (force false: SIGTERM, which
// lets it clean up) or kills it (force true: SIGKILL). On Windows both
// end it at once. A process that is gone already is no error.
func SignalProcess(pid int, force bool) error {
	if pid <= 1 {
		return errors.New("refusing to signal PID 0 or 1")
	}
	return signalProcess(pid, force)
}
