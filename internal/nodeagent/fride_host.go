package nodeagent

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// The fride_host capability (docs/NODES.md, "FrIDE instances"): a native
// node declares it once `fridolin node fride host setup` ran on its
// machine (fride-host.json), with sbx's version, whether sbx is ready,
// the machine's memory, what it keeps for itself and the instances. The
// declaration is read again at most every frideHostEvery, and right
// after an instance changes state. A box never declares it.

// frideHostEvery is how often, at most, the agent reads the declaration
// again on its own.
const frideHostEvery = 60 * time.Second

// frideHostDeclared is the fride_host declaration as last read; nil
// when the node does not offer it.
func (a *Agent) frideHostDeclared() *nodeproto.FrideHostCap {
	a.inst.capMu.Lock()
	defer a.inst.capMu.Unlock()
	return cloneFrideHost(a.inst.cap)
}

// cloneFrideHost is a deep copy of c.
func cloneFrideHost(c *nodeproto.FrideHostCap) *nodeproto.FrideHostCap {
	if c == nil {
		return nil
	}
	out := *c
	out.Instances = slices.Clone(c.Instances)
	for i := range out.Instances {
		out.Instances[i].Repos = slices.Clone(out.Instances[i].Repos)
	}
	return &out
}

// frideHostKick has the agent read the declaration again soon, and tell
// the core when it changed: after an instance changed state.
func (a *Agent) frideHostKick() {
	a.inst.init()
	select {
	case a.inst.refresh <- struct{}{}:
	default:
	}
}

// frideHostRefresh reads the declaration again, unless force is false
// and it did so less than frideHostEvery ago. It reports whether what the
// node declares changed.
func (a *Agent) frideHostRefresh(ctx context.Context, force bool) bool {
	a.inst.init()
	a.inst.refreshMu.Lock()
	defer a.inst.refreshMu.Unlock()
	a.inst.capMu.Lock()
	at := a.inst.capAt
	a.inst.capMu.Unlock()
	if !force && !at.IsZero() && time.Since(at) < frideHostEvery {
		return false
	}
	next := a.frideHostRead(ctx)
	a.inst.capMu.Lock()
	old := a.inst.cap
	a.inst.cap, a.inst.capAt = next, time.Now()
	a.inst.capMu.Unlock()
	switch {
	case old == nil && next != nil:
		a.logf("node: offering fride_host to the core (%s)", frideHostSummary(next))
	case old != nil && next == nil:
		a.logf("node: no longer offering fride_host")
	case old != nil && (old.Ready != next.Ready || old.Problem != next.Problem):
		a.logf("node: fride_host: %s", frideHostSummary(next))
	}
	return !reflect.DeepEqual(old, next)
}

// frideHostSummary is the declaration in a few words, for the log.
func frideHostSummary(c *nodeproto.FrideHostCap) string {
	if !c.Ready {
		return "not ready: " + c.Problem
	}
	return fmt.Sprintf("ready, %d of %d MB available, %d kept, %d instances", c.AvailableMemoryMB, c.TotalMemoryMB, c.ReserveMB, len(c.Instances))
}

// frideHostRead is the declaration now: nil without setup, in a box or
// on a system without Docker Sandboxes.
func (a *Agent) frideHostRead(ctx context.Context) *nodeproto.FrideHostCap {
	if a.Dir == "" || a.Config.Box != nil || !sbxOS() {
		return nil
	}
	host, err := LoadFrideHost(a.Dir)
	if host == nil && err == nil {
		return nil
	}
	c := &nodeproto.FrideHostCap{ReserveMB: a.Config.reserveMB()}
	memory := a.inst.memory
	if memory == nil {
		memory = hostMemory
	}
	total, avail, merr := memory()
	c.TotalMemoryMB, c.AvailableMemoryMB = int(total>>20), int(avail>>20)
	d := a.sbx()
	ctx, cancel := context.WithTimeout(ctx, sbxSettingsTimeout)
	defer cancel()
	var problems []string
	if err != nil {
		problems = append(problems, err.Error())
	}
	if merr != nil {
		problems = append(problems, "the machine's memory: "+merr.Error())
	}
	var statuses map[string]string
	if d == nil {
		problems = append(problems, "sbx is not installed (or not where the agent looks: on the PATH, /opt/homebrew/bin, /usr/local/bin)")
	} else {
		if out, _, err := d.run(ctx, nil, nil, "version"); err == nil {
			c.Version = parseSbxVersion(out)
		}
		_, list, problem := sbxState(ctx, d)
		if problem != "" {
			problems = append(problems, problem)
		} else {
			settings, _, serr := d.run(ctx, nil, nil, "settings", "ls", "--json")
			policy, _, perr := d.run(ctx, nil, nil, "policy", "ls", "--json")
			if serr != nil || perr != nil || !sbxSettingsHold(settings, policy) {
				problems = append(problems, "sbx's global settings are not fridolin's (network policy deny-all, SSH agent forwarding and the browser off): fridolin node fride host setup")
			}
			if host != nil && !sbxHasTemplate(ctx, d, host.Template) {
				problems = append(problems, "sbx has no template "+host.Template+": fridolin node fride host setup")
			}
			statuses = map[string]string{}
			for _, sb := range list {
				statuses[sb.Name] = orUnknown(sb.Status)
			}
		}
	}
	c.Ready = len(problems) == 0
	c.Problem = problemText(strings.Join(problems, "; "))
	o := a.instanceOps()
	for _, st := range listFrideInstances(a.Dir) {
		status := "unknown"
		if statuses != nil {
			status = statuses[st.Sandbox]
		}
		c.Instances = append(c.Instances, o.view(ctx, st, status, a.inst.isStarting(st.Name), a.inst.serveProblem(st.Name)))
	}
	if len(c.Instances) > 32 {
		c.Instances = c.Instances[:32]
	}
	if c.Check() != nil {
		for i := range c.Instances {
			c.Instances[i].Repos = nil
		}
	}
	return c
}

// hostMemory is the machine's memory and what the OS can give now: on
// macOS its free, inactive, purgeable and speculative pages (vm_stat),
// elsewhere what platform.Memory says (Linux: MemAvailable).
func hostMemory() (total, available uint64, err error) {
	total, available, err = platform.Memory()
	if err != nil || runtime.GOOS != "darwin" {
		return total, available, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, verr := exec.CommandContext(ctx, "/usr/bin/vm_stat").Output()
	if verr != nil {
		return total, available, nil
	}
	if avail, ok := parseVMStat(out); ok {
		available = avail
	}
	return total, available, nil
}

// vmStatPageRe is vm_stat's page size, on its first line.
var vmStatPageRe = regexp.MustCompile(`page size of ([0-9]+) bytes`)

// parseVMStat is the bytes macOS can give now, from vm_stat's output:
// its free, inactive, purgeable and speculative pages.
func parseVMStat(out []byte) (uint64, bool) {
	m := vmStatPageRe.FindSubmatch(out)
	if m == nil {
		return 0, false
	}
	page, err := strconv.ParseUint(string(m[1]), 10, 64)
	if err != nil || page == 0 {
		return 0, false
	}
	var pages uint64
	seen := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Pages free", "Pages inactive", "Pages purgeable", "Pages speculative":
			n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), "."), 10, 64)
			if err != nil {
				return 0, false
			}
			pages += n
			seen++
		}
	}
	return pages * page, seen == 4
}
