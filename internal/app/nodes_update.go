package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/skill"
	"github.com/Icewater1337/fridolin/internal/store"
)

// Node updates (docs/NODES.md, "Updates"): the core updates a node's
// fridolin to its own version from the builds in its node-binaries
// folder. internal/nodes does the work; here are where the builds are,
// what keeps a node busy, the lockdown, and the core brain's tool.

// NodeBinariesEnv names another folder of node binaries than the one
// beside the bridge's binary.
const NodeBinariesEnv = "FRIDOLIN_NODE_BINARIES"

// nodeBinariesDir is the folder of node binaries: $FRIDOLIN_NODE_BINARIES,
// else node-binaries beside the bridge's binary (/opt/fridolin in the
// core image, ~/.local/lib/fridolin for the service), "" when neither is
// known.
func nodeBinariesDir(exe string) string {
	if d := os.Getenv(NodeBinariesEnv); d != "" {
		return d
	}
	if exe == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "node-binaries")
}

// frideIdle are FrIDE's thread statuses of work that is not running.
var frideIdle = []string{"done", "failed", "closed", "cancelled", "canceled", "error"}

// nodeBusy says why node id should not be updated now: a FrIDE task of
// the core's runs there, or a backup to a folder on it.
func (b *App) nodeBusy(id string) string {
	if b.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if threads, err := b.store.FrideThreads(ctx, store.FrideSent); err == nil {
			for _, t := range threads {
				if t.Node == id && !slices.Contains(frideIdle, strings.ToLower(t.FrideStatus)) {
					return fmt.Sprintf("FrIDE #%d runs there (%s)", t.ID, orDefault(t.FrideStatus, "started"))
				}
			}
		}
	}
	if b.targets != nil {
		if why := b.targets.busyOn(id); why != "" {
			return why
		}
	}
	return ""
}

// nodeHold refuses node updates in a lockdown (M5), as it refuses exec.
func (b *App) nodeHold() string {
	if b.locked() {
		return "the bridge is in lockdown; unlock it in the dashboard or with !unlock"
	}
	return ""
}

// busyOn says which backup runs to a folder on node id, "" for none.
func (t *targetService) busyOn(node string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, tg := range t.cfgs.Get().Vault.Targets {
		if job := t.jobs[tg.Name]; job != "" && tg.Node == node {
			return "the backup target " + tg.Name + " runs a " + job + " job there"
		}
	}
	return ""
}

// findNode finds a node by its id or its name (any case).
func (b *App) findNode(ref string) (*nodes.Node, error) {
	ref = strings.TrimSpace(ref)
	list, err := b.nodes.Registry().List()
	if err != nil && list == nil {
		return nil, err
	}
	for _, n := range list {
		if n.ID == ref {
			return n, nil
		}
	}
	var found []*nodes.Node
	for _, n := range list {
		if strings.EqualFold(n.Name, ref) && n.Device != nodeproto.DeviceRevoked {
			found = append(found, n)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no node %q", ref)
	case 1:
		return found[0], nil
	}
	return nil, fmt.Errorf("%d nodes are called %q: name one by its id", len(found), ref)
}

// nodeUpdateWait is how long node_update waits for the update to end
// before it says the update goes on.
var nodeUpdateWait = 3 * time.Minute

func (b *App) registerNodeTools(r *mcp.Registry[mcpCaller]) {
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "node_update",
		Available: func(c mcpCaller) bool { return c.core() && b.nodesEnabled },
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Update one of the owner's machines (a node) to this core's fridolin version: the core sends it the signed build of its version, the node swaps it in and restarts, and goes back to its old version by itself if the new one does not connect. Only when the person asks to update a node, or after telling them a node runs an older version and they agree. Refused while the node runs a FrIDE task or a backup, or when the node's update is not approved.",
				InputSchema: schema(map[string]any{
					"node": strProp("The node: its name or id, as !fleet lists it."),
				}, "node"),
			}
		},
		Call: b.guarded(skill.RiskWrite, b.callNodeUpdate),
	})
}

func (b *App) callNodeUpdate(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var args struct {
		Node string `json:"node"`
	}
	if err := json.Unmarshal(raw, &args); err != nil || strings.TrimSpace(args.Node) == "" {
		return mcp.ErrorResult("node is missing: the node's name or id"), nil
	}
	n, err := b.findNode(args.Node)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	to, err := b.nodes.StartUpdate(n.ID, nodes.UpdateOptions{})
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	ctx, cancel := context.WithTimeout(ctx, nodeUpdateWait)
	defer cancel()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return mcp.TextResult(fmt.Sprintf("Updating %s from %s to %s goes on: it restarts and has %d minutes to connect again, else it goes back by itself. !fleet and the dashboard's Nodes page show how it ends.", n.Name, n.Version, to, nodeproto.UpdateTrialMinutes)), nil
		case <-t.C:
		}
		cur, err := b.nodes.Registry().Get(n.ID)
		if err != nil || cur.Update == nil {
			continue
		}
		switch cur.Update.State {
		case nodes.UpdateOK:
			return mcp.TextResult(fmt.Sprintf("%s runs %s now (it ran %s).", n.Name, to, n.Version)), nil
		case nodes.UpdateFailed:
			return mcp.ErrorResult(fmt.Sprintf("Updating %s to %s failed: %s. It runs %s.", n.Name, to, cur.Update.Error, cur.Version)), nil
		}
	}
}
