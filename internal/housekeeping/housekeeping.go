// Package housekeeping finds what the bridge and its children left
// running, the processes that hold CPU or memory and should not, and
// stops what it owns.
//
// Ownership is conservative. A process is the bridge's when it can be
// told apart by what only the bridge does:
//
//   - a brain run's processes carry FRIDOLIN_BRAIN_TOKEN_FILE, naming a
//     file in the bridge's run-tokens folder that exists exactly while the
//     run lasts, in their environment, which every command the run starts
//     inherits, also one that left the run's session;
//   - a run's MCP server is `<the bridge's binary> mcp --run <id>`, and
//     its run's MCP configuration file exists while the run lasts;
//   - the vault's children run with the bridge's own paths: `rclone rcd`
//     on a socket in a fridolin-rc-* folder, `restic` with the cache in
//     <data>/core/restic-cache, `litestream` with a configuration in
//     <data>/core.
//
// Such a process is orphaned when its run is over, or when no bridge is
// its parent any more. Orphans older than MinAge are stopped (SIGTERM,
// SIGKILL after the grace), each with what it started. Nothing else is
// ever stopped without asking: in the core's container, where everything
// running is the core's, a process that is not the bridge's and holds
// more CPU or memory than the thresholds is put to the owner (an
// approval poll); on a native install or a node, the user's own
// processes are not even listed.
package housekeeping

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/platform"
)

// The kinds of findings.
const (
	KindRunOrphan  = "run-orphan"  // a process of a brain run that is over
	KindMCPOrphan  = "mcp-orphan"  // a run's MCP server whose run is over
	KindRcloneRcd  = "rclone-rcd"  // the vault's rclone daemon, no bridge its parent
	KindLitestream = "litestream"  // the vault's Litestream, no bridge its parent
	KindRestic     = "restic"      // a vault restic run, no bridge its parent
	KindViewer     = "tmux-viewer" // an activity viewer's tmux session nobody needs
	KindZombie     = "zombie"      // an ended process nobody collected
	KindHeavy      = "heavy"       // a live process of the bridge above a threshold
	KindForeign    = "foreign"     // in the container: not the bridge's, above a threshold
	KindResticLock = "restic-lock" // a restic lock of a dead process of this host
	KindBridgeLock = "bridge-lock" // bridge.lock naming a dead process
	KindRunRecord  = "run-record"  // a run record whose process group is gone
)

// The actions a finding asks for.
const (
	ActionStop   = "stop"   // the bridge's own: stopped without asking
	ActionAsk    = "ask"    // not the bridge's: stopped only after the owner says yes
	ActionReport = "report" // listed only
	ActionRemove = "remove" // a stale file or lock, removed
)

// Finding is one thing a run found.
type Finding struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	// PID is the process (the topmost one of a tree that goes together),
	// Members every process of it, with Start its start time, which a
	// stop checks again so that a PID given to another process since is
	// left alone.
	PID     int       `json:"pid,omitempty"`
	PPID    int       `json:"ppid,omitempty"`
	Start   time.Time `json:"start,omitzero"`
	Members []Member  `json:"members,omitempty"`
	Name    string    `json:"name,omitempty"`
	Command string    `json:"command,omitempty"`
	// CPUPercent and RSS add up the members; Age is the oldest's.
	CPUPercent float64 `json:"cpu_percent,omitempty"`
	RSS        uint64  `json:"rss,omitempty"`
	AgeSeconds int64   `json:"age_seconds,omitempty"`
	// Owner says whose it is: "run <id> (brain <x>)", "vault", "bridge",
	// "container".
	Owner  string `json:"owner,omitempty"`
	Reason string `json:"reason"`
	// Problem: something the job cannot fix itself, which the owner
	// hears about.
	Problem bool `json:"problem,omitempty"`
	// Outcome is what came of it: stopped, would stop (dry run), asked,
	// failed: …, left (too young), reported.
	Outcome  string `json:"outcome,omitempty"`
	Approval string `json:"approval,omitempty"`
	// Target is what a non-process finding is about: a lock's ID, a
	// file, a tmux session.
	Target string `json:"target,omitempty"`
}

// Member is one process of a finding.
type Member struct {
	PID   int       `json:"pid"`
	Start time.Time `json:"start,omitzero"`
}

// Settings are the job's thresholds and mode.
type Settings struct {
	DryRun bool
	// CPUPercent and RSSBytes make a process heavy (0: no threshold).
	CPUPercent float64
	RSSBytes   uint64
	// MinAge leaves orphans younger than this alone: a run that just
	// ended may still be winding down.
	MinAge time.Duration
	// Container: the bridge runs in the core's container, where every
	// process is the core's.
	Container bool
}

// Ownership is what tells the bridge's processes from others.
type Ownership struct {
	Self int    // the bridge's PID
	Exe  string // the bridge's binary, which runs `mcp --run`
	// RunTokensDir is <data>/run-tokens and CoreDir <data>/core, both
	// absolute.
	RunTokensDir string
	CoreDir      string
	// TokenLive reports whether a run's token file (by its path) still
	// exists; MCPRunLive whether run id still has its MCP configuration.
	TokenLive  func(path string) bool
	MCPRunLive func(id string) bool
}

// Sample is the processes at one moment, with the CPU each used since an
// earlier sample, in percent of one core.
type Sample struct {
	At    time.Time
	Procs []platform.Proc
	CPU   map[int]float64
}

// TakeSample lists the processes twice, gap apart, and computes how much
// CPU each used in between. A process seen only once gets none.
func TakeSample(ctx context.Context, list func() ([]platform.Proc, error), gap time.Duration) (Sample, error) {
	first, err := list()
	if err != nil {
		return Sample{}, err
	}
	t0 := time.Now()
	if gap > 0 {
		select {
		case <-ctx.Done():
			return Sample{}, ctx.Err()
		case <-time.After(gap):
		}
	}
	second, err := list()
	if err != nil {
		return Sample{}, err
	}
	wall := time.Since(t0)
	before := make(map[int]platform.Proc, len(first))
	for _, p := range first {
		before[p.PID] = p
	}
	cpu := map[int]float64{}
	for _, p := range second {
		if b, ok := before[p.PID]; ok && b.Start.Equal(p.Start) && wall > 0 && p.CPU >= b.CPU {
			cpu[p.PID] = float64(p.CPU-b.CPU) / float64(wall) * 100
		}
	}
	return Sample{At: time.Now(), Procs: second, CPU: cpu}, nil
}

// owned is what a process is, when it is the bridge's.
type owned struct {
	kind  string
	owner string
	live  bool // its run lasts, or a bridge is its parent
	why   string
}

// Plan sorts what s shows into findings. env reads a process's
// environment (an error leaves the environment test out).
func Plan(s Sample, env func(pid int) ([]string, error), set Settings, o Ownership) []Finding {
	byPID := make(map[int]platform.Proc, len(s.Procs))
	children := map[int][]int{}
	for _, p := range s.Procs {
		byPID[p.PID] = p
		children[p.PPID] = append(children[p.PPID], p.PID)
	}
	// The bridge and what it runs now.
	bridgeTree := map[int]bool{}
	var walk func(pid int)
	walk = func(pid int) {
		if bridgeTree[pid] {
			return
		}
		bridgeTree[pid] = true
		for _, c := range children[pid] {
			walk(c)
		}
	}
	if o.Self > 0 {
		walk(o.Self)
	}
	uid := -1
	if self, ok := byPID[o.Self]; ok {
		uid = self.UID
	}

	kinds := map[int]owned{}
	for _, p := range s.Procs {
		if p.PID <= 1 || p.PID == o.Self || p.Zombie {
			continue
		}
		// Another user's process is never the bridge's, nor readable.
		if uid >= 0 && p.UID >= 0 && p.UID != uid {
			continue
		}
		if k, ok := classify(p, byPID, bridgeTree, env, o); ok {
			kinds[p.PID] = k
		}
	}

	var out []Finding
	taken := map[int]bool{}
	// Orphans: the topmost orphan of each tree, with all below it.
	pids := make([]int, 0, len(kinds))
	for pid := range kinds {
		pids = append(pids, pid)
	}
	slices.Sort(pids)
	for _, pid := range pids {
		k := kinds[pid]
		if k.live || taken[pid] {
			continue
		}
		// An orphan whose parent is an orphan too goes with its parent.
		if pk, ok := kinds[byPID[pid].PPID]; ok && !pk.live {
			continue
		}
		f := treeFinding(pid, byPID, children, s, taken)
		f.Kind, f.Owner, f.Reason = k.kind, k.owner, k.why
		f.Action = ActionStop
		if time.Duration(f.AgeSeconds)*time.Second < set.MinAge {
			f.Action = ActionReport
			f.Outcome = "left: younger than " + set.MinAge.String()
		}
		out = append(out, f)
	}

	for _, p := range s.Procs {
		if p.PID <= 1 || taken[p.PID] {
			continue
		}
		age := ageOf(p, s.At)
		switch {
		case p.Zombie:
			// The bridge's own zombie is a bug in it; in the container
			// every zombie is the core's (tini, PID 1, collects those it
			// inherits, so one whose parent lives is that parent's).
			if p.PPID == o.Self {
				out = append(out, Finding{Kind: KindZombie, Action: ActionReport, PID: p.PID, PPID: p.PPID, Name: p.Name, Owner: "bridge",
					Reason: "an ended child of the bridge that the bridge has not collected", Problem: true, Outcome: "reported"})
			} else if set.Container && p.PPID > 1 {
				out = append(out, Finding{Kind: KindZombie, Action: ActionReport, PID: p.PID, PPID: p.PPID, Name: p.Name, Owner: "container",
					Reason: fmt.Sprintf("an ended process its parent (PID %d) has not collected", p.PPID), Outcome: "reported"})
			}
			continue
		case !heavy(p, s, set):
			continue
		}
		f := single(p, s)
		switch k, ok := kinds[p.PID]; {
		case p.PID == o.Self || bridgeTree[p.PID] || (ok && k.live):
			f.Kind, f.Action, f.Owner, f.Outcome = KindHeavy, ActionReport, "bridge", "reported"
			if ok {
				f.Owner = k.owner
			}
			f.Reason = heavyReason(p, s, set) + "; it is the bridge's and still in use"
		case set.Container && age >= set.MinAge:
			f.Kind, f.Action, f.Owner = KindForeign, ActionAsk, "container"
			f.Reason = heavyReason(p, s, set) + "; not started by the bridge"
		default:
			continue
		}
		out = append(out, f)
	}
	return out
}

// classify says whether p is the bridge's, and whether it is still in use.
func classify(p platform.Proc, byPID map[int]platform.Proc, bridgeTree map[int]bool, env func(int) ([]string, error), o Ownership) (owned, bool) {
	args := p.Args
	base := p.Base()
	// A child of a bridge: this one, or another bridge on this machine
	// (another data dir, dev mode), whose children are its own.
	underBridge := bridgeTree[p.PID]
	if pp, ok := byPID[p.PPID]; ok && !underBridge && p.PPID > 1 {
		if isFridolin(pp) {
			underBridge = true
		}
	}
	// The run token a process's environment names, when it can be read:
	// ours (under RunTokensDir), another bridge's, or none.
	token, readable := "", false
	if env != nil && o.RunTokensDir != "" {
		if vars, err := env(p.PID); err == nil {
			readable = true
			for _, kv := range vars {
				if v, ok := strings.CutPrefix(kv, "FRIDOLIN_BRAIN_TOKEN_FILE="); ok {
					token = v
				}
			}
		}
	}
	ours := token != "" && under(o.RunTokensDir, token)
	switch {
	case len(args) >= 4 && args[1] == "mcp" && args[2] == "--run" && (args[0] == o.Exe || isFridolinName(base)):
		// Another bridge's run's server names that bridge's token; one
		// whose environment cannot be read is ours only by our binary.
		if token != "" && !ours || token == "" && (readable || args[0] != o.Exe) {
			return owned{}, false
		}
		id := args[3]
		live := bridgeTree[p.PID] || (o.MCPRunLive != nil && o.MCPRunLive(id)) || (ours && o.TokenLive != nil && o.TokenLive(token))
		return owned{kind: KindMCPOrphan, owner: "run " + id, live: live,
			why: "the MCP server of run " + id + ", which is over"}, true
	case base == "rclone" && slices.Contains(args, "rcd") && slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, "/fridolin-rc-") }):
		return owned{kind: KindRcloneRcd, owner: "vault", live: underBridge,
			why: "the vault's rclone daemon, whose bridge is gone"}, true
	case base == "litestream" && o.CoreDir != "" && hasFlagUnder(args, "-config", o.CoreDir):
		return owned{kind: KindLitestream, owner: "vault", live: underBridge,
			why: "the vault's Litestream, whose bridge is gone"}, true
	case base == "restic" && o.CoreDir != "" && flagValue(args, "--cache-dir") == filepath.Join(o.CoreDir, "restic-cache"):
		return owned{kind: KindRestic, owner: "vault", live: underBridge,
			why: "a restic run of the vault, whose bridge is gone"}, true
	}
	if !ours {
		return owned{}, false // nobody's run, or another bridge's
	}
	rel, _ := filepath.Rel(o.RunTokensDir, token)
	brain := filepath.Dir(rel)
	live := bridgeTree[p.PID] || (o.TokenLive != nil && o.TokenLive(token))
	return owned{kind: KindRunOrphan, owner: "a run of brain " + brain, live: live,
		why: "left running by a run of brain " + brain + " that is over"}, true
}

// under reports whether path lies inside dir.
func under(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func isFridolin(p platform.Proc) bool { return isFridolinName(p.Base()) || isFridolinName(p.Name) }

func isFridolinName(name string) bool {
	name = strings.TrimSuffix(strings.ToLower(name), ".exe")
	return name == "fridolin"
}

// flagValue is the value of flag in args, as "--flag v" or "--flag=v".
func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v
		}
	}
	return ""
}

func hasFlagUnder(args []string, flag, dir string) bool {
	v := flagValue(args, flag)
	return v != "" && under(dir, v)
}

func heavy(p platform.Proc, s Sample, set Settings) bool {
	return (set.CPUPercent > 0 && s.CPU[p.PID] >= set.CPUPercent) || (set.RSSBytes > 0 && p.RSS >= set.RSSBytes)
}

func heavyReason(p platform.Proc, s Sample, set Settings) string {
	var parts []string
	if set.CPUPercent > 0 && s.CPU[p.PID] >= set.CPUPercent {
		parts = append(parts, fmt.Sprintf("%.0f%% CPU (threshold %.0f%%)", s.CPU[p.PID], set.CPUPercent))
	}
	if set.RSSBytes > 0 && p.RSS >= set.RSSBytes {
		parts = append(parts, fmt.Sprintf("%s memory (threshold %s)", Bytes(p.RSS), Bytes(set.RSSBytes)))
	}
	return strings.Join(parts, " and ")
}

func ageOf(p platform.Proc, now time.Time) time.Duration {
	if p.Start.IsZero() {
		return 0
	}
	return now.Sub(p.Start)
}

// single is a finding of p alone.
func single(p platform.Proc, s Sample) Finding {
	return Finding{PID: p.PID, PPID: p.PPID, Start: p.Start, Members: []Member{{PID: p.PID, Start: p.Start}},
		Name: p.Name, Command: Command(p), CPUPercent: round1(s.CPU[p.PID]), RSS: p.RSS, AgeSeconds: int64(ageOf(p, s.At) / time.Second)}
}

// treeFinding is a finding of root and every process below it, which it
// marks taken.
func treeFinding(root int, byPID map[int]platform.Proc, children map[int][]int, s Sample, taken map[int]bool) Finding {
	f := single(byPID[root], s)
	f.Members = nil
	var add func(pid int)
	add = func(pid int) {
		if taken[pid] {
			return
		}
		taken[pid] = true
		p := byPID[pid]
		f.Members = append(f.Members, Member{PID: pid, Start: p.Start})
		if pid != root {
			f.CPUPercent = round1(f.CPUPercent + s.CPU[pid])
			f.RSS += p.RSS
			if a := int64(ageOf(p, s.At) / time.Second); a > f.AgeSeconds {
				f.AgeSeconds = a
			}
		}
		for _, c := range children[pid] {
			add(c)
		}
	}
	add(root)
	return f
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }

// Command is p's command line for a report: the program and its first
// arguments, at most 160 characters. A run's prompt is never in its
// arguments, but other commands' may be anything.
func Command(p platform.Proc) string {
	if len(p.Args) == 0 {
		return p.Name
	}
	c := strings.Join(p.Args, " ")
	if len(c) > 160 {
		c = c[:157] + "…"
	}
	return c
}

// Bytes says n in MB or GB.
func Bytes(n uint64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%d MB", n>>20)
}
