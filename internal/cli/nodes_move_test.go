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

// decodeOne decodes stdout as exactly one JSON document.
func decodeOne(t *testing.T, stdout []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(stdout))
	if err := dec.Decode(v); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		t.Fatalf("stdout holds more than one JSON document (%v):\n%s", err, stdout)
	}
}

// Under --yes --json, stdout carries one JSON document and the progress
// goes to stderr (#663).
func TestNodesMoveJSONStdoutIsJSON(t *testing.T) {
	old := moveFollowEvery
	moveFollowEvery = time.Millisecond
	t.Cleanup(func() { moveFollowEvery = old })
	report := nodes.MoveReport{Source: nodes.MoveEnd{Name: "mini"}, Target: nodes.MoveEnd{Name: "icebase"}, Version: "v1", Blocking: []string{}}

	f := &fakeMoveAPI{report: report}
	var out, errb bytes.Buffer
	if code := nodesMove(f.call, "aaaaaaaaaaaa", "bbbbbbbbbbbb", true, false, true, &out, &errb); code != 0 {
		t.Fatalf("move: exit %d: %s %s", code, out.String(), errb.String())
	}
	var got moveOutput
	decodeOne(t, out.Bytes(), &got)
	if got.Check.Target.Name != "icebase" || got.Move == nil || got.Move.State != nodes.MoveDone || len(got.Move.Steps) != 2 {
		t.Errorf("the document: %+v", got)
	}
	for _, want := range []string{"Move m-20261024-1200-beef runs", "release: mini stops the core", "does not answer", "answers again", "Move m-20261024-1200-beef: done."} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, errb.String())
		}
	}

	// --no-wait: the record as the move started.
	f = &fakeMoveAPI{report: report}
	out.Reset()
	if code := nodesMove(f.call, "aaaaaaaaaaaa", "bbbbbbbbbbbb", true, true, true, &out, io.Discard); code != 0 || f.asks != 0 {
		t.Fatalf("no-wait: exit %d, %d asks: %s", code, f.asks, out.String())
	}
	got = moveOutput{}
	decodeOne(t, out.Bytes(), &got)
	if got.Move == nil || got.Move.ID != "m-20261024-1200-beef" || got.Move.State != nodes.MovePreparing {
		t.Errorf("no-wait: %+v", got.Move)
	}

	// Blocked: the report alone, and no move.
	f = &fakeMoveAPI{report: nodes.MoveReport{Blocking: []string{"icebase runs version v0 and the core v1"}}}
	out.Reset()
	if code := nodesMove(f.call, "aaaaaaaaaaaa", "bbbbbbbbbbbb", true, false, true, &out, io.Discard); code != 1 || len(f.posts) != 0 {
		t.Fatalf("blocked: exit %d, posts %v", code, f.posts)
	}
	got = moveOutput{}
	decodeOne(t, out.Bytes(), &got)
	if got.Move != nil || len(got.Check.Blocking) != 1 {
		t.Errorf("blocked: %+v", got)
	}
}
