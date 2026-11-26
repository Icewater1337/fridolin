package app

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// A FrIDE thread whose task is gone from FrIDE closes here at fride_close,
// no longer blocks fride_stop, and force stops past a live thread.
func TestFrideStaleThreads(t *testing.T) {
	t.Parallel()
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	h := newServedHarness(t, frideInstancesConfig)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	if w := h.Do("PUT", "/api/gitlab", map[string]any{"base_url": "https://gitlab.example.com", "token": testGitLabToken, "name": "Jane Doe", "email": "jane@example.com"}); w.Code != http.StatusOK {
		t.Fatalf("the GitLab connection: %d %s", w.Code, w.Body.String())
	}
	mini := h.fakeHostNode("mini", h.StartNodes(), 32768, 16000, 6144)

	// 1. fride_close of a task FrIDE no longer has closes the thread.
	h.coreSays("go", step("fride_task", `{"message":"a","instance":"work"}`), step("fride_status", `{"thread":1}`))
	mini.setGone(true)
	reply := h.coreSays("close", step("fride_close", `{"thread":1}`))
	if !strings.Contains(reply, "no longer exists in FrIDE") || strings.Contains(reply, "failed") {
		t.Fatalf("fride_close of a gone task:\n%s", reply)
	}

	// 2. A gone task does not block fride_stop: reconciled on the spot.
	mini.setGone(false)
	h.coreSays("go", step("fride_task", `{"message":"b","instance":"work"}`), step("fride_status", `{"thread":2}`))
	mini.setGone(true)
	reply = h.coreSays("stop", step("fride_stop", `{"instance":"work"}`))
	if !strings.Contains(reply, "Stopped the FrIDE instance work") {
		t.Fatalf("fride_stop with a gone thread:\n%s", reply)
	}

	// 3. A live thread blocks fride_stop, and force stops anyway.
	mini.setGone(false)
	h.coreSays("go", step("fride_task", `{"message":"c","instance":"work"}`), step("fride_status", `{"thread":3}`))
	reply = h.coreSays("stop", step("fride_stop", `{"instance":"work"}`), step("fride_stop", `{"instance":"work","force":true}`))
	if !strings.Contains(reply, "[core:fride_stop failed] the FrIDE instance work has open threads (#3)") || !strings.Contains(reply, "Stopped the FrIDE instance work") {
		t.Fatalf("fride_stop with and without force:\n%s", reply)
	}
}

// Only FrIDE's own answer for a task it does not have counts as gone: a
// node's not-found for an instance, or any other error, keeps the thread.
func TestTaskGone(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		err  error
		want bool
	}{
		{nodeproto.Errorf(nodeproto.CodeNotFound, "no task %q", "gmail-access"), true},
		{nodeproto.Errorf(nodeproto.CodeNotFound, "no FrIDE instance fridolin on this node"), false},
		{nodeproto.Errorf(nodeproto.CodeUnavailable, "no task %q", "x"), false},
		{errors.New(`no task "x"`), false},
		{nil, false},
	} {
		if got := taskGone(c.err); got != c.want {
			t.Errorf("taskGone(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
