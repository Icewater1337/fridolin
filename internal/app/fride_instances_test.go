package app

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// frideInstancesConfig names two instances: work, on mini only, with the
// GitLab identity, and fridolin, anywhere, with GitHub's.
const frideInstancesConfig = `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}, "fride": {"news": "all", "instances": {
	"work": {"description": "work repositories", "repos": ["https://gitlab.example.com/group/app.git"], "identities": ["gitlab"], "egress": ["registry.example.com"], "nodes": ["mini"]},
	"fridolin": {"description": "fridolin and FrIDE themselves", "repos": ["https://github.com/example/fridolin.git"], "identities": ["github"]}
}}}`

// instanceStarts are the starts of instance name the node got.
func instanceStarts(f *fakeHost, name string) []nodeproto.FrideInstanceStartParams {
	var out []nodeproto.FrideInstanceStartParams
	for _, p := range f.Starts() {
		if p.Name == name {
			out = append(out, p)
		}
	}
	return out
}

// The core's side of FrIDE instances against a node that offers
// fride_host: fride_task starts the instance it names and hands it the
// work with the instance on every call; its news, replies, status and
// close stay on it; placement shrinks a start when nothing else may stop;
// the dashboard lists, stops and starts them; a new token reaches a
// running instance; and every start and stop is in the audit.
func TestFrideInstances(t *testing.T) {
	t.Parallel()
	optionsOf(t).waits.frideRetry = time.Hour
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	h := newServedHarness(t, frideInstancesConfig)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	if w := h.Do("PUT", "/api/gitlab", map[string]any{"base_url": "https://gitlab.example.com", "token": testGitLabToken, "name": "Jane Doe", "email": "jane@example.com"}); w.Code != http.StatusOK {
		t.Fatalf("the GitLab connection: %d %s", w.Code, w.Body.String())
	}
	addr := h.StartNodes()
	// 9856 MB beyond its reserve.
	mini := h.fakeHostNode("mini", addr, 32768, 16000, 6144)

	reply := h.coreSays("what runs", step("fride_instances", `{}`))
	for _, want := range []string{`"name": "work"`, `"description": "work repositories"`, `"name": "fridolin"`, `"node": "mini"`, `"free_mb": 9856`, `"medium": 6144`} {
		if !strings.Contains(reply, want) {
			t.Fatalf("fride_instances lacks %s:\n%s", want, reply)
		}
	}

	// fride_task starts work (medium) with its identity, then hands it the
	// task.
	reply = h.coreSays("fix the login", step("fride_task", `{"message":"Fix the login","instance":"work"}`))
	if !strings.Contains(reply, "Handed to FrIDE on mini/work as FrIDE #1") || !strings.Contains(reply, "Started the FrIDE instance work on mini with 6144 MB.") {
		t.Fatalf("fride_task:\n%s", reply)
	}
	starts := instanceStarts(mini, "work")
	if len(starts) != 1 {
		t.Fatalf("work started %d times", len(starts))
	}
	p := starts[0]
	if p.MemoryMB != 6144 || !slices.Equal(p.Repos, []string{"https://gitlab.example.com/group/app.git"}) || !slices.Equal(p.Egress, []string{"registry.example.com"}) ||
		len(p.Identities) != 1 || p.Identities[0].ID != "gitlab" || p.Identities[0].Host != "gitlab.example.com" || p.Identities[0].Token != testGitLabToken ||
		p.Git == nil || p.Git.Name != "Jane Doe" || p.Git.Email != "jane@example.com" {
		t.Errorf("work's start: %+v", p)
	}
	if !mini.calledWith(nodeproto.MethodFrideTask, "work") {
		t.Errorf("the task did not go to work: %q", mini.Calls())
	}

	// Its news reaches the chat, naming where it is; news of another FrIDE's
	// thread 1 on the node is not its.
	mini.emit(nodeproto.FrideEvent{Instance: "fridolin", Event: "question", ThreadID: 1, Text: "Not yours?", Status: "needs_reply"})
	mini.emit(nodeproto.FrideEvent{Instance: "work", Event: "question", ThreadID: 1, Text: "Which provider?", Status: "needs_reply"})
	h.WaitSent(harnessDM, "❓ *FrIDE #1* asks (on mini/work):\n\nWhich provider?")
	if slices.ContainsFunc(h.SentTo(harnessDM), func(m string) bool { return strings.Contains(m, "Not yours?") }) {
		t.Error("news of fridolin's thread 1 went to work's")
	}

	reply = h.coreSays("the SSO one",
		step("fride_reply", `{"thread":1,"text":"the SSO one"}`),
		step("fride_status", `{"thread":1}`),
		step("fride_move", `{"thread":1,"node":"mini"}`),
		step("fride_stop", `{"instance":"work"}`),
		step("fride_task", `{"message":"x","instance":"nope"}`),
		step("fride_task", `{"message":"x","instance":"work","node":"mini"}`))
	for _, want := range []string{
		"Passed on to FrIDE #1 on mini/work",
		`"instance": "work"`,
		"[core:fride_move failed] FrIDE #1 is work of the FrIDE instance work, and stays on it",
		"[core:fride_stop failed] the FrIDE instance work has open threads (#1)",
		`[core:fride_task failed] there is no FrIDE instance "nope": the instances are fridolin, work`,
		"[core:fride_task failed] give node or instance, not both",
	} {
		if !strings.Contains(reply, want) {
			t.Fatalf("lacks %q:\n%s", want, reply)
		}
	}
	for _, m := range []string{nodeproto.MethodFrideReply, nodeproto.MethodFrideStatus} {
		if !mini.calledWith(m, "work") {
			t.Errorf("%s did not go to work: %q", m, mini.Calls())
		}
	}

	// No room for large, and work has an open thread: fridolin gets small.
	reply = h.coreSays("start fridolin", step("fride_spawn", `{"instance":"fridolin","size":"large","reason":"a heavy build"}`))
	if !strings.Contains(reply, "Started the FrIDE instance fridolin on mini with 3072 MB. That is less than the 12288 MB asked") ||
		!strings.Contains(reply, "It has no github identity") {
		t.Fatalf("fride_spawn:\n%s", reply)
	}
	if s := instanceStarts(mini, "fridolin"); len(s) != 1 || len(s[0].Identities) != 0 {
		t.Errorf("fridolin's start: %+v", s)
	}

	// The dashboard lists them.
	w := h.Do("GET", "/api/fride", nil)
	assertGolden(t, "api/get-fride-instances.golden", append([]byte("GET /api/fride\n"), h.scrubber().maskKeys("node_id", "updated", "at").response(t, w)...))

	// It stops one: an unknown 404, one with open threads 409 unless forced.
	if w := h.Do("POST", "/api/fride/instances/nope/stop", nil); w.Code != http.StatusNotFound {
		t.Errorf("stopping nope: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("POST", "/api/fride/instances/work/stop", map[string]any{}); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "open threads (#1)") {
		t.Errorf("stopping work: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("POST", "/api/fride/instances/work/stop", map[string]any{"force": true}); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"ok":true}` {
		t.Fatalf("stopping work by force: %d %s", w.Code, w.Body.String())
	}
	if in, _ := mini.Instance("work"); in.State != nodeproto.FrideInstanceStopped {
		t.Fatalf("work is %s", in.State)
	}

	// A reply starts it again where its work is.
	reply = h.coreSays("and add a test", step("fride_reply", `{"thread":1,"text":"and add a test"}`))
	if !strings.Contains(reply, "Passed on to FrIDE #1 on mini/work") || len(instanceStarts(mini, "work")) != 2 {
		t.Fatalf("a reply to stopped work (%d starts):\n%s", len(instanceStarts(mini, "work")), reply)
	}

	// A new GitLab token reaches the running instance.
	if w := h.Do("PUT", "/api/gitlab", map[string]any{"token": testGitLabToken2}); w.Code != http.StatusOK {
		t.Fatalf("the new token: %d %s", w.Code, w.Body.String())
	}
	h.WaitFor("work given the new token", func() bool {
		s := instanceStarts(mini, "work")
		last := s[len(s)-1]
		return len(last.Identities) == 1 && last.Identities[0].Token == testGitLabToken2
	})
	if s := instanceStarts(mini, "fridolin"); len(s) != 1 {
		t.Errorf("fridolin, which nothing changed for, was started again: %d", len(s))
	}

	// Closing archives the workspace through the instance.
	reply = h.coreSays("close it", step("fride_close", `{"thread":1}`))
	if !strings.Contains(reply, "Closed task login on mini/work (branch fix/login)") {
		t.Fatalf("fride_close:\n%s", reply)
	}
	checkBundle(t, filepath.Join(h.DataDir(), "vault", "workspaces", "login.fride.tar.gz"))
	for _, m := range []string{nodeproto.MethodFrideClose, nodeproto.MethodFrideExport, nodeproto.MethodFrideRead, nodeproto.MethodFrideDelete, nodeproto.MethodFrideDrop} {
		if !mini.calledWith(m, "work") {
			t.Errorf("%s did not go to work: %q", m, mini.Calls())
		}
	}
	for _, c := range mini.Calls() {
		if strings.HasSuffix(c, "@") && !strings.HasPrefix(c, "fride.instance.") {
			t.Errorf("a call for no instance: %s", c)
		}
	}

	// With no open thread it stops.
	reply = h.coreSays("stop work", step("fride_stop", `{"instance":"work"}`))
	if !strings.Contains(reply, "Stopped the FrIDE instance work on mini") {
		t.Fatalf("fride_stop:\n%s", reply)
	}

	// The dashboard starts one, placed as fride_spawn places it.
	if w := h.Do("POST", "/api/fride/instances/work/start", map[string]any{"size": "huge"}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("a start at size huge: %d %s", w.Code, w.Body.String())
	}
	w = h.Do("POST", "/api/fride/instances/work/start", map[string]any{"size": "small"})
	var view frideInstanceView
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.State != "running" || view.Node != "mini" || view.RunningMemoryMB != 3072 {
		t.Fatalf("starting work: %d %s", w.Code, w.Body.String())
	}

	// Not in a lockdown.
	if _, err := h.b.setLockdown(context.Background(), true, "dashboard"); err != nil {
		t.Fatal(err)
	}
	if w := h.Do("POST", "/api/fride/instances/fridolin/start", nil); w.Code != http.StatusForbidden {
		t.Errorf("a start in a lockdown: %d %s", w.Code, w.Body.String())
	}
	reply = h.coreSays("start it", step("fride_spawn", `{"instance":"fridolin","reason":"x"}`), step("fride_instances", `{}`))
	if !strings.Contains(reply, "[core:fride_spawn failed] the bridge is in lockdown") || strings.Contains(reply, "[core:fride_instances failed]") {
		t.Fatalf("in a lockdown:\n%s", reply)
	}

	var lines []string
	for _, l := range h.auditOf("fride_instance") {
		lines = append(lines, l["id"].(string)+" "+l["by"].(string)+" "+l["outcome"].(string)+": "+l["detail"].(string))
		if strings.Contains(l["detail"].(string), testGitLabToken) {
			t.Errorf("the audit has a token: %v", l)
		}
	}
	want := []string{
		"work core brain ok: started on mini (" + mini.ID + ") with 6144 MB: FrIDE #1",
		"fridolin core brain ok: started on mini (" + mini.ID + ") with 3072 MB, less than the 12288 MB asked: a heavy build",
		"work dashboard ok: stopped on mini (" + mini.ID + "): stopped on the dashboard, with its open threads",
		"work core brain ok: started on mini (" + mini.ID + ") with 6144 MB: FrIDE #1",
		"work core brain ok: stopped on mini (" + mini.ID + "): the core brain asked",
		"work dashboard ok: started on mini (" + mini.ID + ") with 3072 MB: started on the dashboard",
	}
	if !slices.Equal(lines, want) {
		t.Errorf("the audit:\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// An instance with no open thread and no news stops after its
// idle_stop_minutes, and one with an open thread does not; one set to
// never (-1) stays.
func TestFrideInstanceIdleStop(t *testing.T) {
	t.Parallel()
	o := optionsOf(t)
	o.waits.frideRetry = time.Hour
	o.waits.frideIdleCheck, o.waits.frideIdleUnit = 50*time.Millisecond, 100*time.Millisecond
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}, "fride": {"instances": {
		"work": {"idle_stop_minutes": 3}, "site": {"idle_stop_minutes": 3}, "keep": {"idle_stop_minutes": -1}}}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	addr := h.StartNodes()
	mini := h.fakeHostNode("mini", addr, 65536, 60000, 6144)

	reply := h.coreSays("start them",
		step("fride_task", `{"message":"Fix it","instance":"work"}`),
		step("fride_spawn", `{"instance":"site","size":"small","reason":"ahead"}`),
		step("fride_spawn", `{"instance":"keep","size":"small","reason":"ahead"}`))
	if !strings.Contains(reply, "as FrIDE #1") || strings.Contains(reply, "failed]") {
		t.Fatalf("starting:\n%s", reply)
	}
	h.WaitFor("site stopped as idle", func() bool {
		in, _ := mini.Instance("site")
		return in.State == nodeproto.FrideInstanceStopped
	})
	time.Sleep(time.Second)
	for _, name := range []string{"work", "keep"} {
		if in, _ := mini.Instance(name); in.State != nodeproto.FrideInstanceRunning {
			t.Errorf("%s is %s", name, in.State)
		}
	}
	idle := h.auditOf("fride_instance")
	if i := slices.IndexFunc(idle, func(l map[string]any) bool { return l["by"] == "idle" }); i < 0 || idle[i]["id"] != "site" || idle[i]["detail"] != "stopped on mini ("+mini.ID+"): idle for 3 minutes" {
		t.Errorf("the audit has no idle stop of site: %v", idle)
	}
}
