package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

// TestNodesUpdateWaitsAsLongAsTheCore: a box's update takes longer than
// the CLI's own wait (its updater runs hourly), and the CLI keeps waiting
// until the core's wait_until, saying it is still going (#296).
func TestNodesUpdateWaitsAsLongAsTheCore(t *testing.T) {
	poll, wait, grace, progress := nodeUpdatePoll, nodeUpdateWait, nodeUpdateGrace, nodeUpdateProgress
	t.Cleanup(func() {
		nodeUpdatePoll, nodeUpdateWait, nodeUpdateGrace, nodeUpdateProgress = poll, wait, grace, progress
	})
	nodeUpdatePoll, nodeUpdateWait, nodeUpdateGrace, nodeUpdateProgress = time.Millisecond, 20*time.Millisecond, 20*time.Millisecond, 10*time.Millisecond

	until := time.Now().Add(time.Second)
	done := time.Now().Add(150 * time.Millisecond)
	call := func(method, path string, body, out any) bool {
		var v any = httpapi.NodeUpdateResponse{ID: "n1", To: "v2"}
		if method == http.MethodGet {
			u := &nodes.UpdateStatus{State: nodes.UpdateRestarting, To: "v2", WaitUntil: &until}
			if time.Now().After(done) {
				u.State = nodes.UpdateOK
			}
			v = httpapi.NodesResponse{Nodes: []nodes.View{{Node: &nodes.Node{ID: "n1", Name: "icebase", Update: u}}}}
		}
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out) == nil
	}
	var stdout, stderr bytes.Buffer
	if code := nodesUpdate([]string{"n1"}, false, false, false, call, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "icebase (n1) runs v2 now.") {
		t.Errorf("stdout %q: no success line", stdout.String())
	}
	if !strings.Contains(stdout.String(), "icebase (n1): still updating (restarting") {
		t.Errorf("stdout %q: no progress line", stdout.String())
	}
}

// TestNodesUpdateByName: the core answers an update of a node named by
// its name with the node's id, which the wait follows (#251).
func TestNodesUpdateByName(t *testing.T) {
	poll, wait := nodeUpdatePoll, nodeUpdateWait
	t.Cleanup(func() { nodeUpdatePoll, nodeUpdateWait = poll, wait })
	nodeUpdatePoll, nodeUpdateWait = time.Millisecond, time.Second
	var asked string
	call := func(method, path string, body, out any) bool {
		var v any = httpapi.NodeUpdateResponse{ID: "n1", To: "v2"}
		if method == http.MethodGet {
			v = httpapi.NodesResponse{Nodes: []nodes.View{{Node: &nodes.Node{ID: "n1", Name: "icebase", Update: &nodes.UpdateStatus{State: nodes.UpdateOK, To: "v2"}}}}}
		} else {
			asked = path
		}
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out) == nil
	}
	var stdout, stderr bytes.Buffer
	if code := nodesUpdate([]string{"IceBase"}, false, false, false, call, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if asked != "/api/nodes/IceBase/update" || !strings.Contains(stdout.String(), "icebase (n1) runs v2 now.") {
		t.Errorf("asked %s, stdout %q", asked, stdout.String())
	}
}
