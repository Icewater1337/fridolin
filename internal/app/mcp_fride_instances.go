package app

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/mcp/tools"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/skill"
	"github.com/Icewater1337/fridolin/internal/store"
)

// The core brain's tools for FrIDE instances (docs/NODES.md, "The core
// and FrIDE instances"): a core run has them while nodes are on and
// fride.instances names some. fride_task takes an instance too
// (mcp_fride.go).

// frideInstanceTools reports whether the run has the instance tools.
func (b *App) frideInstanceTools(c mcpCaller) bool {
	return b.frideTools(c) && len(b.cfgs.Get().Fride.Instances) > 0
}

// frideInstanceList names the instances for a tool's description, each
// with what it is for and its repositories: "work (work repositories;
// repos app)".
func (b *App) frideInstanceList() string {
	cfg := b.cfgs.Get().Fride.Instances
	var out []string
	for _, name := range slices.Sorted(maps.Keys(cfg)) {
		ic := cfg[name]
		var about []string
		if d := strings.TrimSpace(ic.Description); d != "" {
			about = append(about, d)
		}
		if len(ic.Repos) > 0 {
			repos := make([]string, len(ic.Repos))
			for i, r := range ic.Repos {
				repos[i] = strings.TrimSuffix(path.Base(strings.TrimRight(r, "/")), ".git")
			}
			about = append(about, "repos "+strings.Join(repos, ", "))
		}
		if len(about) > 0 {
			name += " (" + strings.Join(about, "; ") + ")"
		}
		out = append(out, name)
	}
	return strings.Join(out, ", ")
}

func instanceProp() map[string]any {
	return tools.Str("The FrIDE instance, by name, as fride_instances lists it.")
}

// registerFrideInstanceTools registers fride_instances, which only
// reads, and fride_spawn and fride_stop, which change what runs on a node
// and so are write (tools.Guarded).
func (b *App) registerFrideInstanceTools(r *tools.Set[mcpCaller]) {
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "fride_instances",
		Available: b.frideInstanceTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "List the FrIDE instances: each a FrIDE of its own for one trust domain, in a sandbox on one of the owner's machines, with what it is for, its repositories and git identities, where it runs (or that it does not), its memory and its open threads; and the machines that run instances, with the memory each has free. The instances now: " + b.frideInstanceList() + ".",
				InputSchema: tools.Schema(map[string]any{}),
			}
		},
		Call: b.callFrideInstances,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "fride_spawn",
		Available: b.frideInstanceTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Start a FrIDE instance ahead of work, or make a running one that has no open thread another size. fride_task starts the one it needs on its own (medium), so this is for when the work needs another size: small (3 GB) for code and unit tests, medium (6 GB) with the project's stack, large (12 GB) for a heavy stack. The machine with the most room takes it; idle instances of other trust domains may stop to make room, and when even then none has room it gets a smaller size, which the answer says.",
				InputSchema: tools.Schema(map[string]any{
					"instance": instanceProp(),
					"size":     map[string]any{"type": "string", "enum": []string{frideSizeSmall, frideSizeMedium, frideSizeLarge}, "description": "small, medium (the default) or large."},
					"reason":   tools.Str("Why, in a few words: it goes into the audit log."),
				}, "instance", "reason"),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callFrideSpawn),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "fride_stop",
		Available: b.frideInstanceTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Stop a FrIDE instance that has no open thread, when the person asks: its machine gets its memory back, its disk stays, and it starts again when work needs it. Instances stop on their own after a while idle.",
				InputSchema: tools.Schema(map[string]any{"instance": instanceProp()}, "instance"),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callFrideStop),
	})
}

// instanceArgs reads an instance tool's arguments.
type instanceArgs struct {
	Instance string `json:"instance"`
	Size     string `json:"size"`
	Reason   string `json:"reason"`
}

func readInstanceArgs(raw json.RawMessage) (instanceArgs, *mcp.CallResult) {
	var a instanceArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, mcp.ErrorResult("unreadable arguments: " + err.Error())
	}
	a.Instance, a.Size, a.Reason = strings.TrimSpace(a.Instance), strings.ToLower(strings.TrimSpace(a.Size)), strings.TrimSpace(a.Reason)
	if a.Instance == "" {
		return a, mcp.ErrorResult("instance is missing: the FrIDE instance's name, as fride_instances lists it")
	}
	if !validFrideSize(a.Size) {
		return a, mcp.ErrorResult(fmt.Sprintf("size %q: small, medium or large", a.Size))
	}
	return a, nil
}

// frideInstanceToolView is one instance of fride_instances.
type frideInstanceToolView struct {
	frideInstanceView
	// Threads are its open threads' numbers.
	Threads []int64 `json:"threads"`
}

// frideHostToolView is a machine that runs instances.
type frideHostToolView struct {
	Node        string   `json:"node"`
	Ready       bool     `json:"ready"`
	Problem     string   `json:"problem,omitempty"`
	TotalMB     int      `json:"total_mb"`
	AvailableMB int      `json:"available_mb"`
	ReserveMB   int      `json:"reserve_mb"`
	FreeMB      int      `json:"free_mb"`
	Running     []string `json:"running"`
}

func (b *App) callFrideInstances(ctx context.Context, _ mcpCaller, _ json.RawMessage) (*mcp.CallResult, error) {
	open, err := b.fride.inst.openThreads(ctx)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	views := b.fride.inst.views()
	list := []frideInstanceToolView{}
	for _, v := range b.fride.inst.instanceViews(views, open) {
		tv := frideInstanceToolView{frideInstanceView: v, Threads: []int64{}}
		for _, t := range open[v.Name] {
			tv.Threads = append(tv.Threads, t.ID)
		}
		list = append(list, tv)
	}
	hosts := []frideHostToolView{}
	for _, v := range views {
		fh := v.Effective.FrideHost
		if v.Device != nodeproto.DeviceApproved || fh == nil {
			continue
		}
		h := frideHostToolView{Node: v.Name, Ready: fh.Ready && v.Online, Problem: fh.Problem, TotalMB: fh.TotalMemoryMB, AvailableMB: fh.AvailableMemoryMB,
			ReserveMB: fh.ReserveMB, FreeMB: max(fh.AvailableMemoryMB-fh.ReserveMB, 0), Running: []string{}}
		if !v.Online {
			h.Problem = "offline"
		}
		for _, in := range fh.Instances {
			if in.State == nodeproto.FrideInstanceRunning || in.State == nodeproto.FrideInstanceStarting {
				h.Running = append(h.Running, fmt.Sprintf("%s (%s, %d MB)", in.Name, in.State, in.MemoryMB))
			}
		}
		hosts = append(hosts, h)
	}
	out := map[string]any{"instances": list, "hosts": hosts, "sizes_mb": frideSizeMB}
	data, _ := json.MarshalIndent(out, "", "  ")
	return mcp.TextResult(string(data)), nil
}

func (b *App) callFrideSpawn(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readInstanceArgs(raw)
	if bad != nil {
		return bad, nil
	}
	if a.Reason == "" {
		return mcp.ErrorResult("reason is missing: why the instance starts, in a few words"), nil
	}
	b.fride.start()
	s, err := b.fride.inst.start(ctx, a.Instance, a.Size, "core brain", a.Reason)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	b.fride.kickQueue()
	return mcp.TextResult(s.Text()), nil
}

func (b *App) callFrideStop(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readInstanceArgs(raw)
	if bad != nil {
		return bad, nil
	}
	text, err := b.fride.inst.stop(ctx, a.Instance, "core brain", "the core brain asked", false)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	return mcp.TextResult(text), nil
}

// frideInstanceView is one instance of GET /api/fride: its config, and
// where and how it runs.
type frideInstanceView struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Repos       []string `json:"repos"`
	Identities  []string `json:"identities"`
	Egress      []string `json:"egress"`
	Nodes       []string `json:"nodes"`
	// MemoryMB is the config's fixed memory, 0 when the core decides.
	MemoryMB int `json:"memory_mb"`
	// IdleStopMinutes is the effective idle stop; 0 is never.
	IdleStopMinutes int `json:"idle_stop_minutes"`
	// State is the node's word on it, "" when no node has it.
	State string `json:"state"`
	// Node and NodeID are where it is; empty when no node has it.
	Node            string `json:"node"`
	NodeID          string `json:"node_id"`
	RunningMemoryMB int    `json:"running_memory_mb"`
	OpenThreads     int    `json:"open_threads"`
	Problem         string `json:"problem"`
}

// instanceViews are the configured instances, sorted by name, as views
// and open find them.
func (i *frideInstances) instanceViews(views []nodes.View, open map[string][]store.FrideThread) []frideInstanceView {
	cfg := i.configured()
	out := []frideInstanceView{}
	for _, name := range slices.Sorted(maps.Keys(cfg)) {
		out = append(out, i.instanceView(name, views, open))
	}
	return out
}

// instanceView is instance name's view.
func (i *frideInstances) instanceView(name string, views []nodes.View, open map[string][]store.FrideThread) frideInstanceView {
	ic := i.configured()[name]
	v := frideInstanceView{Name: name, Description: ic.Description, Repos: orEmpty(ic.Repos), Identities: orEmpty(ic.Identities),
		Egress: orEmpty(ic.Egress), Nodes: orEmpty(ic.Nodes), MemoryMB: ic.MemoryMB, IdleStopMinutes: ic.IdleStop(), OpenThreads: len(open[name])}
	if w, ok := locate(name, views); ok {
		v.State, v.Node, v.NodeID, v.Problem = w.in.State, w.v.Name, w.v.ID, w.in.Problem
		if w.in.State == nodeproto.FrideInstanceRunning || w.in.State == nodeproto.FrideInstanceStarting {
			v.RunningMemoryMB = w.in.MemoryMB
		}
		if !w.v.Online && v.Problem == "" {
			v.Problem = w.v.Name + " is offline"
		}
	}
	if v.State != nodeproto.FrideInstanceRunning && v.Problem == "" {
		i.st.Lock()
		if f, ok := i.failed[name]; ok {
			v.Problem = "the last start failed: " + f.why
		}
		i.st.Unlock()
	}
	return v
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return slices.Clone(s)
}
