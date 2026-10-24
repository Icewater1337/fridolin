package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/controlcli"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

// fakeMoveAPI answers the move routes: a report, a move, and its record,
// which is away (the core stops) for one ask and then done.
type fakeMoveAPI struct {
	report nodes.MoveReport
	asks   int
	posts  []string
}

func (f *fakeMoveAPI) call(method, path string, body, out any) error {
	put := func(v any) error {
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out)
	}
	switch {
	case path == "/api/nodes/move-check":
		return put(f.report)
	case path == "/api/nodes/move":
		f.posts = append(f.posts, body.(httpapi.MoveRequest).Target)
		return put(httpapi.MoveResponse{MoveRecord: nodes.MoveRecord{ID: "m-20261024-1200-beef", State: nodes.MovePreparing}})
	case strings.HasPrefix(path, "/api/nodes/moves/"):
		f.asks++
		rec := nodes.MoveRecord{ID: "m-20261024-1200-beef", State: nodes.MoveHandedOff, Steps: []nodeproto.CoreMoveStep{{At: time.Now(), Step: "release", Detail: "mini stops the core"}}}
		switch f.asks {
		case 1:
		case 2:
			return controlcli.ErrBridgeDown
		default:
			rec.State, rec.Detail = nodes.MoveDone, "the core runs on icebase"
			rec.Steps = append(rec.Steps, nodeproto.CoreMoveStep{At: time.Now(), Step: "done", Detail: "the core runs on icebase"})
		}
		return put(rec)
	}
	return errors.New("no route " + path)
}

func TestNodesMove(t *testing.T) {
	old := moveFollowEvery
	moveFollowEvery = time.Millisecond
	t.Cleanup(func() { moveFollowEvery = old })
	f := &fakeMoveAPI{report: nodes.MoveReport{Source: nodes.MoveEnd{Name: "mini"}, Target: nodes.MoveEnd{Name: "icebase"}, Version: "v1", Blocking: []string{}}}
	var out bytes.Buffer
	// Without --yes, the pre-flight only.
	if code := nodesMove(f.call, "aaaaaaaaaaaa", "bbbbbbbbbbbb", false, false, false, &out, io.Discard); code != 0 || len(f.posts) != 0 || !strings.Contains(out.String(), "A dry run") {
		t.Fatalf("dry run: %d %v %s", code, f.posts, out.String())
	}
	out.Reset()
	if code := nodesMove(f.call, "aaaaaaaaaaaa", "bbbbbbbbbbbb", true, false, false, &out, io.Discard); code != 0 {
		t.Fatalf("move: exit %d: %s", code, out.String())
	}
	for _, want := range []string{"Move m-20261024-1200-beef runs", "release: mini stops the core", "does not answer", "answers again", "done: the core runs on icebase", "Move m-20261024-1200-beef: done."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Count(out.String(), "release: mini stops the core") != 1 {
		t.Errorf("a step printed twice:\n%s", out.String())
	}
	// A blocked move does not start.
	f = &fakeMoveAPI{report: nodes.MoveReport{Blocking: []string{"icebase runs version v0 and the core v1"}}}
	out.Reset()
	if code := nodesMove(f.call, "aaaaaaaaaaaa", "bbbbbbbbbbbb", true, false, false, &out, io.Discard); code != 1 || len(f.posts) != 0 || !strings.Contains(out.String(), "It cannot run:") {
		t.Errorf("blocked: %d %v %s", code, f.posts, out.String())
	}
}
