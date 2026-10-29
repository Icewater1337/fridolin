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
	"github.com/Icewater1337/fridolin/internal/store"
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
	// Only as an update: a stop the core has yet to hear of stands.
	if s := instanceStarts(mini, "work"); !s[len(s)-1].UpdateOnly {
		t.Error("the new token went as a start, which undoes a stop")
	}
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

// Work for an instance that cannot start waits, its chat told why, and
// goes once a node has room; a node's start that failed is not tried
// again at once, and the queue's own tries are not in the audit.
func TestFrideInstanceWaits(t *testing.T) {
	t.Parallel()
	optionsOf(t).waits.frideRetry = 100 * time.Millisecond
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}, "fride": {"instances": {
		"work": {"memory_mb": 8192}, "site": {"nodes": ["mini"]}}}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	addr := h.StartNodes()
	mini := h.fakeHostNode("mini", addr, 16384, 10144, 6144)
	mini.mu.Lock()
	mini.failStart = "the FrIDE template is not built"
	mini.mu.Unlock()

	reply := h.coreSays("fix it", step("fride_task", `{"message":"Fix it","instance":"work"}`), step("fride_task", `{"message":"Build the site","instance":"site"}`))
	if !strings.Contains(reply, "FrIDE #1 waits: the FrIDE instance work cannot take it now (no node has room for the FrIDE instance work (8192 MB): mini has 4000 MB free beyond its reserve)") ||
		!strings.Contains(reply, "FrIDE #2 waits: the FrIDE instance site cannot take it now (starting the FrIDE instance site on mini: failed: the FrIDE template is not built)") {
		t.Fatalf("fride_task:\n%s", reply)
	}
	h.WaitSent(harnessDM, "⏳ *FrIDE #1* waits: the FrIDE instance work cannot take it now (no node has room")
	h.WaitSent(harnessDM, "⏳ *FrIDE #2* waits: the FrIDE instance site cannot take it now (starting the FrIDE instance site on mini: failed: the FrIDE template is not built)")

	big := h.fakeHostNode("big", addr, 65536, 40000, 6144)
	h.WaitSent(harnessDM, "🛠️ *FrIDE #1* went to big/work now.")
	if s := instanceStarts(big, "work"); len(s) != 1 || s[0].MemoryMB != 8192 {
		t.Errorf("work on big: %+v", s)
	}
	time.Sleep(time.Second)
	if s := instanceStarts(mini, "site"); len(s) != 1 {
		t.Errorf("site's start on mini that failed was tried %d times", len(s))
	}
	if len(instanceStarts(big, "site")) != 0 {
		t.Error("site, which may run on mini only, started on big")
	}
	var failed []string
	for _, l := range h.auditOf("fride_instance") {
		if strings.HasPrefix(l["outcome"].(string), "failed") {
			failed = append(failed, l["id"].(string)+" by "+l["by"].(string))
		}
	}
	if !slices.Equal(failed, []string{"work by core brain", "site by core brain"}) {
		t.Errorf("the audit's failed starts: %q", failed)
	}
	w := h.Do("GET", "/api/fride", nil)
	if !strings.Contains(w.Body.String(), `"problem":"the last start failed: starting the FrIDE instance site on mini: failed: the FrIDE template is not built"`) {
		t.Errorf("GET /api/fride does not say why site did not start:\n%s", w.Body.String())
	}
}

// A start with no room stops an idle instance of another trust domain on
// the node to make room, and says so.
func TestFrideInstanceMakesRoom(t *testing.T) {
	t.Parallel()
	optionsOf(t).waits.frideRetry = time.Hour
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}, "fride": {"instances": {"work": {}, "site": {}}}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	addr := h.StartNodes()
	// 7000 MB beyond its reserve.
	mini := h.fakeHostNode("mini", addr, 16384, 13144, 6144)

	reply := h.coreSays("start them", step("fride_spawn", `{"instance":"site","size":"small","reason":"ahead"}`), step("fride_task", `{"message":"Fix it","instance":"work"}`))
	if !strings.Contains(reply, "Started the FrIDE instance work on mini with 6144 MB. To make room, the idle instance site stopped.") {
		t.Fatalf("making room:\n%s", reply)
	}
	if in, _ := mini.Instance("site"); in.State != nodeproto.FrideInstanceStopped {
		t.Errorf("site is %s", in.State)
	}
	if !slices.ContainsFunc(h.auditOf("fride_instance"), func(l map[string]any) bool {
		return l["id"] == "site" && l["by"] == "core" && l["detail"] == "stopped on mini ("+mini.ID+"): to make room for work"
	}) {
		t.Errorf("the audit: %v", h.auditOf("fride_instance"))
	}
}

// The core sends the published FrIDE template with each start once it
// knows where its images are (its image's registry, and the template's
// own tag, else its version); a node that built no template of its own
// gets no instance from a core that knows none, and says why.
func TestFrideInstanceTemplate(t *testing.T) {
	t.Parallel()
	optionsOf(t).waits.frideRetry = 100 * time.Millisecond
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}, "fride": {"instances": {"work": {"nodes": ["mini"]}}}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	addr := h.StartNodes()
	mini := h.fakeHostNode("mini", addr, 32768, 16000, 6144)
	mini.mu.Lock()
	mini.cap.NeedsTemplate = true
	mini.mu.Unlock()
	mini.declare()
	h.WaitFor("mini needs a template", func() bool {
		n, err := h.b.nodes.Registry().Get(mini.ID)
		return err == nil && n.Effective().FrideHost != nil && n.Effective().FrideHost.NeedsTemplate
	})

	reply := h.coreSays("fix it", step("fride_task", `{"message":"Fix it","instance":"work"}`))
	if !strings.Contains(reply, "mini: it built no FrIDE template, and this core does not know where its images are published (FRIDOLIN_BOX_REGISTRY)") {
		t.Fatalf("fride_task:\n%s", reply)
	}
	if s := instanceStarts(mini, "work"); len(s) != 0 {
		t.Fatalf("work started on mini without a template: %+v", s)
	}

	setenv(t, "FRIDOLIN_BOX_REGISTRY", "ghcr.io/you")
	setenv(t, "FRIDOLIN_FRIDE_SANDBOX_TAG", "t-0123456789ab")
	h.WaitSent(harnessDM, "🛠️ *FrIDE #1* went to mini/work now.")
	if s := instanceStarts(mini, "work"); len(s) != 1 || s[0].Template != "ghcr.io/you/fridolin-fride-sandbox:t-0123456789ab" {
		t.Errorf("work's starts: %+v", s)
	}
}

func TestFrideSandboxTemplate(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		registry, tag, version, want string
	}{
		{"ghcr.io/you", "t-0123456789ab", "v1.2.3", "ghcr.io/you/fridolin-fride-sandbox:t-0123456789ab"},
		{"ghcr.io/you/", "", "v1.2.3-4-gabcdef12", "ghcr.io/you/fridolin-fride-sandbox:v1.2.3-4-gabcdef12"},
		{"", "t-0123456789ab", "v1.2.3", ""},
		{"ghcr.io/you", "", "", ""},
		{"ghcr.io/you", "", "1.0+local", ""},
	} {
		env := map[string]string{"FRIDOLIN_BOX_REGISTRY": c.registry, "FRIDOLIN_FRIDE_SANDBOX_TAG": c.tag}
		if got := frideSandboxTemplate(func(n string) string { return env[n] }, c.version); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
}

// A node whose instances' agents cannot log in to Claude gets no new
// instance and no work: the work waits, saying why and what fixes it,
// and the owner is told once. Once the node has the token, the waiting
// work goes there. An instance that runs without a login is started
// again in place once its node has the token, which logs it in; while
// its node has none, it is stopped there and started on a node that has
// one, unless open threads keep it there, which the work is told.
func TestFrideInstanceNoClaudeLogin(t *testing.T) {
	t.Parallel()
	optionsOf(t).waits.frideRetry = 100 * time.Millisecond
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}, "fride": {"instances": {"site": {}, "docs": {}}}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	addr := h.StartNodes()
	const why = "no Claude token is stored for FrIDE's agents; there: fridolin node secrets set claude.oauth.token, with the token claude setup-token prints"
	ice := h.fakeHostNode("ice", addr, 65536, 40000, 6144)
	seen := func(f *fakeHost, host string) {
		t.Helper()
		h.WaitFor(f.name+" declares no_login "+host, func() bool {
			n, err := h.b.nodes.Registry().Get(f.ID)
			return err == nil && n.Declared.FrideHost != nil && n.Declared.FrideHost.NoLogin == host
		})
	}
	ice.setNoLogin(why, nil)
	seen(ice, why)

	reply := h.coreSays("build", step("fride_task", `{"message":"Build the site","instance":"site"}`), step("fride_task", `{"message":"Build it again","instance":"site"}`))
	if !strings.Contains(reply, "FrIDE #1 waits: the FrIDE instance site cannot take it now (the FrIDE instance site cannot start: no node can start FrIDE instances now (ice: its instances' agents cannot log in to Claude: "+why+"))") {
		t.Fatalf("fride_task:\n%s", reply)
	}
	if n := len(instanceStarts(ice, "site")); n != 0 {
		t.Fatalf("site started %d times on ice", n)
	}
	h.WaitSent(harnessDM, "⚠️ FrIDE instances on ice cannot log in to Claude, so the core starts none there and hands them no work: "+why+".")
	told := func() int {
		n := 0
		for _, m := range h.SentTo(harnessDM) {
			if strings.Contains(m, "FrIDE instances on ice cannot log in to Claude") {
				n++
			}
		}
		return n
	}

	// The token arrives: the waiting work goes there, and its agents log in.
	ice.setNoLogin("", nil)
	h.WaitSent(harnessDM, "🛠️ *FrIDE #1* went to ice/site now.")
	h.WaitSent(harnessDM, "🛠️ *FrIDE #2* went to ice/site now.")
	if in, _ := ice.Instance("site"); in.State != nodeproto.FrideInstanceRunning || in.NoLogin != "" {
		t.Errorf("site on ice: %+v", in)
	}

	// docs runs on ice without a login, as it did before the node had the
	// token: work for it starts it again there, which logs it in.
	ice.setNoLogin("", map[string]string{"docs": why})
	h.WaitFor("docs running on ice without a login", func() bool {
		w, ok := locate("docs", h.b.fride.inst.views())
		return ok && w.running() && w.in.NoLogin != ""
	})
	reply = h.coreSays("docs", step("fride_task", `{"message":"Write the docs","instance":"docs"}`))
	if !strings.Contains(reply, "Handed to FrIDE on ice/docs as FrIDE #3") {
		t.Fatalf("docs on ice:\n%s", reply)
	}
	if s := instanceStarts(ice, "docs"); len(s) != 1 {
		t.Errorf("docs was started %d times on ice, not once to log it in", len(s))
	}
	if in, _ := ice.Instance("docs"); in.NoLogin != "" {
		t.Errorf("docs on ice still has no login: %+v", in)
	}

	// ice loses the token while docs again runs without a login and has no
	// open thread (closed): docs moves to mini, which has one.
	th, err := h.b.store.GetFrideThread(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	th.State = store.FrideClosed
	if err := h.b.store.SaveFrideThread(context.Background(), th); err != nil {
		t.Fatal(err)
	}
	ice.setNoLogin(why, map[string]string{"docs": why})
	seen(ice, why)
	mini := h.fakeHostNode("mini", addr, 32768, 16000, 6144)
	reply = h.coreSays("docs again", step("fride_task", `{"message":"Write more docs","instance":"docs"}`))
	if !strings.Contains(reply, "Handed to FrIDE on mini/docs as FrIDE #4") || !strings.Contains(reply, "Started the FrIDE instance docs on mini") {
		t.Fatalf("docs to mini:\n%s", reply)
	}
	if in, _ := ice.Instance("docs"); in.State != nodeproto.FrideInstanceStopped {
		t.Errorf("docs was not stopped on ice: %+v", in)
	}

	// site runs on ice without a login and has open threads there: work
	// for it waits, and says why.
	ice.setNoLogin(why, map[string]string{"site": why})
	h.WaitFor("site running on ice without a login", func() bool {
		w, ok := locate("site", h.b.fride.inst.views())
		return ok && w.in.NoLogin != ""
	})
	reply = h.coreSays("site again", step("fride_task", `{"message":"Fix the site","instance":"site"}`))
	if !strings.Contains(reply, "has open threads on ice, which cannot log in FrIDE's agents to Claude: "+why) {
		t.Fatalf("site with open threads on ice:\n%s", reply)
	}
	if n := told(); n != 1 {
		t.Errorf("the owner was told %d times about ice", n)
	}

	// ice gets the token again: the waiting work starts site again
	// there, which logs its agents in, and goes to it.
	ice.setNoLogin("", nil)
	h.WaitSent(harnessDM, "🛠️ *FrIDE #5* went to ice/site now.")
	if in, _ := ice.Instance("site"); in.NoLogin != "" {
		t.Errorf("site on ice still has no login: %+v", in)
	}

	// Without work waiting, the refresh gives an instance that runs
	// without a login on a node with the token an update, which logs its
	// agents in.
	mini.setNoLogin("", map[string]string{"docs": why})
	h.WaitFor("docs running on mini without a login", func() bool {
		w, ok := locate("docs", h.b.fride.inst.views())
		return ok && w.v.ID == mini.ID && w.in.NoLogin != ""
	})
	before := len(instanceStarts(mini, "docs"))
	h.b.fride.inst.refreshAll(context.Background(), false)
	if s := instanceStarts(mini, "docs"); len(s) != before+1 || !s[len(s)-1].UpdateOnly {
		t.Errorf("docs's starts on mini: %d, then %+v", before, s)
	}
	if in, _ := mini.Instance("docs"); in.NoLogin != "" {
		t.Errorf("docs on mini still has no login: %+v", in)
	}
}
