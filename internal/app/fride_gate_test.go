package app

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// The M6 gate (docs/PLAN.md) end to end with the real FrIDE: built from
// the checkout FRIDOLIN_FRIDE_SRC names, run as `fride serve
// --under-fridolin` on scratch homes, paired with in-process node agents
// joined to a dev-mode bridge, and driven by the core brain's FrIDE tools
// through the fake Claude CLI acting as an MCP client.
//
// What needs FrIDE's real orchestrator (Claude deciding what a message
// means) is played by a stand-in `claude` on FrIDE's PATH that answers each
// orchestrator turn with a scripted reply: ask a question, create a task.
// Everything after that decision is FrIDE's own: the inbox thread, the
// task's worktree and branch, the webhook with its signature, the
// rationale, export, import and the workspace's removal. No coding agent
// runs (startAgent is false), so the work a task's agent would do, and the
// news it would give, are not part of this test; a failed thread, which
// needs the orchestrator to fail five times, is a signed synthetic webhook.

// fride is one FrIDE: a scratch home with one repository, a stand-in
// claude, and `fride serve --under-fridolin`.
type frideServer struct {
	bin, home, url, replies string
}

// frideSource is the FrIDE checkout to build, or a skip.
func frideBinary(t *testing.T) string {
	t.Helper()
	src := os.Getenv("FRIDOLIN_FRIDE_SRC")
	if src == "" {
		t.Skip("set FRIDOLIN_FRIDE_SRC to a FrIDE checkout to run the M6 gate against the real FrIDE")
	}
	if _, err := os.Stat(filepath.Join(src, "companion_cli.go")); err != nil {
		t.Skipf("%s is not a FrIDE checkout with companion mode: %v", src, err)
	}
	for _, tool := range []string{"go", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	bin := filepath.Join(t.TempDir(), "fride")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = src
	build.Env = append(os.Environ(), "HOME="+goToolHome)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building FrIDE: %v\n%s", err, out)
	}
	return bin
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=you@example.com", "-c", "user.name=Test", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func freePortNumber(t *testing.T) int {
	t.Helper()
	_, port, _ := net.SplitHostPort(freePort(t))
	n, _ := strconv.Atoi(port)
	return n
}

// startFrideServer runs FrIDE under root/name. Its repository api is a new
// one, or a clone of origin.
func startFrideServer(t *testing.T, bin, root, name, origin string) *frideServer {
	t.Helper()
	dir := filepath.Join(root, name)
	f := &frideServer{bin: bin, home: filepath.Join(dir, "home"), replies: filepath.Join(dir, "replies")}
	for _, d := range []string{f.home, f.replies, filepath.Join(dir, "repos"), filepath.Join(dir, "ws"), filepath.Join(dir, "bin"), filepath.Join(dir, "user")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(dir, "repos", "api")
	if origin == "" {
		if err := os.MkdirAll(repo, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# api\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "init", "-q")
		runGit(t, repo, "add", "-A")
		runGit(t, repo, "commit", "-qm", "init")
	} else {
		runGit(t, filepath.Join(dir, "repos"), "clone", "-q", origin, "api")
	}
	port := freePortNumber(t)
	cfg := map[string]any{
		"version": 1, "name": name, "workspaceRoot": filepath.Join(dir, "ws"),
		"baseFolders": []string{filepath.Join(dir, "repos")}, "coreRepos": []string{"api"}, "maxSlots": 2,
		"defaultProfile": "local",
		"profiles": map[string]any{"local": map[string]any{"entry": "api", "services": map[string]any{
			"api": map[string]any{"repo": "api", "run": "python3 -m http.server {{port}}", "port": freePortNumber(t)},
		}}},
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(f.home, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// The orchestrator's Claude: the Nth turn answers replies/N.json, any
	// other an empty turn.
	stub := "#!/bin/sh\ncat > /dev/null\nD=" + shellQuote(dir) + "\nn=$(cat \"$D/count\" 2>/dev/null || echo 0)\nn=$((n+1))\necho $n > \"$D/count\"\n" +
		"r='{\"say\":\"\",\"needsUser\":false,\"actions\":[]}'\n[ -f \"$D/replies/$n.json\" ] && r=$(cat \"$D/replies/$n.json\")\n" +
		"printf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"s1\",\"result\":\"\",\"structured_output\":%s}\\n' \"$r\"\n"
	if err := os.WriteFile(filepath.Join(dir, "bin", "claude"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	git, _ := exec.LookPath("git")
	f.url = fmt.Sprintf("http://127.0.0.1:%d", port)
	cmd := exec.Command(bin, "serve", "--under-fridolin", "--port", fmt.Sprint(port))
	cmd.Env = []string{
		"FRIDE_HOME=" + f.home, "HOME=" + filepath.Join(dir, "user"),
		"PATH=" + filepath.Join(dir, "bin") + string(os.PathListSeparator) + filepath.Dir(git) + string(os.PathListSeparator) + "/usr/bin:/bin",
	}
	logf, err := os.Create(filepath.Join(dir, "serve.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		logf.Close()
		if t.Failed() {
			out, _ := os.ReadFile(logf.Name())
			t.Logf("FrIDE %s's log:\n%s", name, out)
		}
	})
	testutil.WaitFor(t, 20*time.Second, "FrIDE "+name+" to answer", func() bool {
		resp, err := http.Get(f.url + "/api/companion/info")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusUnauthorized
	})
	return f
}

// orchestrate scripts the orchestrator's turn n.
func (f *frideServer) orchestrate(t *testing.T, n int, reply string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.replies, fmt.Sprintf("%d.json", n)), []byte(reply), 0o600); err != nil {
		t.Fatal(err)
	}
}

// pairCode is what `fride pair-companion --json` prints.
func (f *frideServer) pairCode(t *testing.T) string {
	t.Helper()
	cmd := exec.Command(f.bin, "pair-companion", "--json")
	cmd.Env = append(os.Environ(), "FRIDE_HOME="+f.home)
	out, err := cmd.Output()
	var code struct {
		Code string `json:"code"`
	}
	if err != nil || json.Unmarshal(out, &code) != nil || code.Code == "" {
		t.Fatalf("fride pair-companion: %v %s", err, out)
	}
	return code.Code
}

// task asks FrIDE about a task with the node's token.
func (f *frideServer) task(t *testing.T, stateDir, id string) nodeproto.FrideTask {
	t.Helper()
	tok, err := os.ReadFile(filepath.Join(stateDir, nodeagent.FrideTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, f.url+"/api/companion/tasks/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var task nodeproto.FrideTask
	_ = json.NewDecoder(resp.Body).Decode(&task)
	return task
}

// frideNode joins a node named name to the bridge, pairs it with fs and
// runs its agent; the owner approves it and what it offers, FrIDE
// included.
func (h *harness) frideNode(name, addr string, fs *frideServer) (id, stateDir string) {
	h.t.Helper()
	t := h.t
	w := h.Do("POST", "/api/nodes/join-code", map[string]any{})
	var jc struct{ Code string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &jc) != nil {
		t.Fatalf("join-code: %d %s", w.Code, w.Body.String())
	}
	stateDir = t.TempDir()
	j, err := nodeagent.Join(context.Background(), nodeagent.JoinOptions{Dir: stateDir, Core: addr, Code: jc.Code, Name: name, Capabilities: nodeproto.Capabilities{Host: true}, AndroidBuild: noAndroidBuild, Version: "test", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	info, err := nodeagent.PairFride(context.Background(), nodeagent.FridePairOptions{Dir: stateDir, URL: fs.url, Code: fs.pairCode(t)})
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode != "companion" || strings.Join(info.Repos, ",") != "api" {
		t.Fatalf("FrIDE %s after pairing: %+v", name, info)
	}
	agent, err := nodeagent.Load(stateDir, "test")
	if err != nil {
		t.Fatal(err)
	}
	agent.Logf, agent.HeartbeatEvery, agent.FrideCheckEvery = t.Logf, 200*time.Millisecond, 200*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = agent.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	h.WaitFor(name+" online, offering FrIDE", func() bool {
		n, err := h.b.nodes.Registry().Get(j.ID)
		return err == nil && h.b.nodes.Online(j.ID) && n.Declared.Fride != nil
	})
	for _, step := range []string{"approve", "approve-commands"} {
		if w := h.Do("POST", "/api/nodes/"+j.ID+"/"+step, nil); w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", step, name, w.Code, w.Body.String())
		}
	}
	h.WaitFor(name+"'s agent to hear of the approval", func() bool { return agent.State().Approved.Fride != nil })
	return j.ID, stateDir
}

// coreSays has the core brain play steps for a message in your DM, and
// returns its answer: the message to the DM with what each step got.
func (h *harness) coreSays(text string, steps ...testutil.FakeMCPStep) string {
	h.t.Helper()
	data, _ := json.Marshal(steps)
	setenv(h.t, "FRIDOLIN_FAKE_MCP", string(data))
	before := len(h.SentTo(harnessDM))
	h.Inject(inbound{JID: harnessDM, Text: text + " " + testutil.FakeMCPTrigger})
	var got string
	testutil.WaitFor(h.t, 60*time.Second, "the core brain's answer to "+text, func() bool {
		for _, m := range h.SentTo(harnessDM)[before:] {
			if strings.Contains(m, "[core:") {
				got = m
				return true
			}
		}
		return false
	})
	return got
}

func step(tool, args string) testutil.FakeMCPStep {
	return testutil.FakeMCPStep{Brain: "core", Tool: tool, Args: json.RawMessage(args)}
}

func TestFrideGate(t *testing.T) {
	t.Parallel()
	bin := frideBinary(t)
	optionsOf(t).waits.frideRetry = 200 * time.Millisecond
	prompts := filepath.Join(t.TempDir(), "prompts.log")
	setenv(t, "FRIDOLIN_FAKE_PROMPT_LOG", prompts)
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	root := t.TempDir()
	laptopFride := startFrideServer(t, bin, root, "laptop", "")
	miniFride := startFrideServer(t, bin, root, "mini", filepath.Join(root, "laptop", "repos", "api"))

	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	addr := h.StartNodes()
	laptop, laptopDir := h.frideNode("laptop", addr, laptopFride)
	mini, miniDir := h.frideNode("mini", addr, miniFride)
	t.Logf("nodes: laptop %s, mini %s", laptop, mini)

	// The core brain has the FrIDE tools, and its prompt the rule.
	reply := h.coreSays("what can you do?", testutil.FakeMCPStep{Brain: "core"})
	if !strings.Contains(reply, "fride_task,fride_status,fride_reply,fride_move,fride_close") {
		t.Fatalf("the core's tools:\n%s", reply)
	}
	if log, _ := os.ReadFile(prompts); !strings.Contains(string(log), "Anything that changes code goes to FrIDE") {
		t.Errorf("the core brain's prompt lacks the code rule")
	}

	// A task from WhatsApp: it goes to the laptop, whose FrIDE asks one
	// question; the question comes to the chat.
	laptopFride.orchestrate(t, 1, `{"say":"Which empty state, the no-results one or the error one?","needsUser":true,"actions":[]}`)
	laptopFride.orchestrate(t, 2, `{"say":"Starting health.","needsUser":false,"actions":[{"kind":"create_task","name":"health","task":"health","branch":"feat/health","repos":[{"name":"api"}],"startAgent":false}]}`)
	reply = h.coreSays("Add a health endpoint to the api, on the laptop", step("fride_task", `{"message":"Add a health endpoint to the api","repos":["api"],"node":"laptop"}`))
	if !strings.Contains(reply, "[core:fride_task] Handed to FrIDE on laptop as FrIDE #1") {
		t.Fatalf("fride_task:\n%s", reply)
	}
	question := h.WaitSent(harnessDM, "❓ *FrIDE #1* asks (on laptop)")
	if !strings.Contains(question, "Which empty state, the no-results one or the error one?") {
		t.Errorf("the question in the chat:\n%s", question)
	}

	// The owner answers in the chat; the core brain, which sees FrIDE's
	// question in the chat's history, passes it on.
	reply = h.coreSays("the no-results one", step("fride_reply", `{"thread":1,"text":"the no-results one"}`))
	if !strings.Contains(reply, "[core:fride_reply] Passed on to FrIDE #1 on laptop") {
		t.Fatalf("fride_reply:\n%s", reply)
	}
	if log, _ := os.ReadFile(prompts); !strings.Contains(string(log), `"from":"FrIDE (its news, relayed by the bridge)","text":"❓ *FrIDE #1* asks`) {
		t.Errorf("the core brain did not see FrIDE's question in the chat's history:\n%s", log)
	}
	started := h.WaitSent(harnessDM, "🛠️ *FrIDE #1* started on laptop: task health, branch feat/health.")
	t.Logf("relayed: %s", started)
	reply = h.coreSays("how is it going?", step("fride_status", `{"thread":1}`))
	if !strings.Contains(reply, `"branch": "feat/health"`) || !strings.Contains(reply, `"node": "laptop"`) {
		t.Errorf("fride_status:\n%s", reply)
	}

	// !move through the vault: the bundle lands in workspaces/moves/, the
	// task continues on the mini, and is gone from the laptop.
	h.Inject(inbound{JID: harnessDM, Text: "!move 1 mini"})
	moved := ""
	testutil.WaitFor(t, 2*time.Minute, "the move", func() bool {
		for _, m := range h.SentTo(harnessDM) {
			if strings.Contains(m, "*FrIDE #1* moved") || strings.Contains(m, "*FrIDE #1* did not move") {
				moved = m
				return true
			}
		}
		return false
	})
	if !strings.Contains(moved, "🚚 *FrIDE #1* moved from laptop to mini: task health continues there.") {
		t.Fatalf("!move: %s", moved)
	}
	vault := filepath.Join(h.DataDir(), "vault", "workspaces")
	checkBundle(t, filepath.Join(vault, "moves", "health.bundle"))
	if task := miniFride.task(t, miniDir, "health"); !task.Exists || task.Branch != "feat/health" {
		t.Errorf("health on the mini: %+v", task)
	}
	if task := laptopFride.task(t, laptopDir, "health"); task.Exists {
		t.Errorf("health is still on the laptop: %+v", task)
	}

	// Closing it archives the workspace into the vault and removes it from
	// the mini.
	reply = h.coreSays("health is finished, close it", step("fride_close", `{"thread":1}`))
	if !strings.Contains(reply, "[core:fride_close] Closed task health on mini (branch feat/health).") || !strings.Contains(reply, "archived in the vault as vault/workspaces/health.fride.tar.gz") {
		t.Fatalf("fride_close after the move:\n%s", reply)
	}
	checkBundle(t, filepath.Join(vault, "health.fride.tar.gz"))
	if task := miniFride.task(t, miniDir, "health"); task.Exists {
		t.Errorf("health's workspace is still on the mini: %+v", task)
	}

	// A second task, on the laptop, which reports done with its branch and
	// rationale when it is closed.
	laptopFride.orchestrate(t, 3, `{"say":"Starting docs.","needsUser":false,"actions":[{"kind":"create_task","name":"docs","task":"docs","branch":"feat/docs","repos":[{"name":"api"}],"startAgent":false}]}`)
	reply = h.coreSays("Write the api's docs on the laptop", step("fride_task", `{"message":"Write the api's docs","node":"laptop"}`))
	if !strings.Contains(reply, "as FrIDE #2") {
		t.Fatalf("the second fride_task:\n%s", reply)
	}
	h.WaitSent(harnessDM, "🛠️ *FrIDE #2* started on laptop: task docs, branch feat/docs.")
	// A failed thread needs five failed turns of the orchestrator: it is a
	// synthetic webhook, signed with the laptop's token.
	postSignedWebhook(t, laptopDir, map[string]any{"event": "failed", "thread_id": 2, "task_id": "docs", "text": "FrIDE inbox #2 · failed\nthe message could not be read", "status": "failed"})
	h.WaitSent(harnessDM, "⚠️ *FrIDE #2* failed on laptop.")
	reply = h.coreSays("docs are done, close them", step("fride_close", `{"thread":2}`))
	if !strings.Contains(reply, "Closed task docs on laptop (branch feat/docs). Rationale: ") {
		t.Fatalf("fride_close:\n%s", reply)
	}
	// FrIDE says done at its next look at the task, every 15 seconds.
	var done string
	testutil.WaitFor(t, 40*time.Second, "done in the chat", func() bool {
		for _, m := range h.SentTo(harnessDM) {
			if strings.Contains(m, "✅ *FrIDE #2* is done on laptop: task docs, branch feat/docs.") {
				done = m
				return true
			}
		}
		return false
	})
	if !strings.Contains(done, "Rationale: ") || !strings.Contains(done, "docs.md") {
		t.Errorf("done without the rationale:\n%s", done)
	}
	checkBundle(t, filepath.Join(vault, "docs.fride.tar.gz"))
	if task := laptopFride.task(t, laptopDir, "docs"); task.Exists {
		t.Errorf("docs' workspace is still on the laptop: %+v", task)
	}

	// Work for a machine that does not offer FrIDE yet waits; once the
	// mini is tagged gpu, it goes there and the chat hears it.
	reply = h.coreSays("train it on the gpu box", step("fride_task", `{"message":"Speed up the tests","node":"gpu"}`))
	if !strings.Contains(reply, "FrIDE #3 waits") {
		t.Fatalf("fride_task with no machine:\n%s", reply)
	}
	h.WaitSent(harnessDM, `⏳ *FrIDE #3* waits: no node with FrIDE can take it now (no online node offers FrIDE that is or is tagged "gpu")`)
	if w := h.Do("PUT", "/api/nodes/"+mini+"/tags", map[string]any{"tags": []string{"macos", "gpu"}}); w.Code != http.StatusOK {
		t.Fatalf("tags: %d %s", w.Code, w.Body.String())
	}
	h.WaitSent(harnessDM, "🛠️ *FrIDE #3* went to mini now.")
}

// checkBundle checks that path is a whole gzip'd tar, as FrIDE exports.
func checkBundle(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("the vault has no %s: %v", path, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		names = append(names, h.Name)
	}
	if len(names) == 0 {
		t.Fatalf("%s holds no files", path)
	}
	t.Logf("%s: %d files, %s", filepath.Base(path), len(names), strings.Join(names[:min(len(names), 6)], ", "))
}

// postSignedWebhook posts event to the node's FrIDE webhook, signed with
// its token as FrIDE signs.
func postSignedWebhook(t *testing.T, stateDir string, event map[string]any) {
	t.Helper()
	cfg, err := nodeagent.LoadConfig(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := os.ReadFile(filepath.Join(stateDir, nodeagent.FrideTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(event)
	req, _ := http.NewRequest(http.MethodPost, "http://"+cfg.Fride.Webhook+"/fride", strings.NewReader(string(body)))
	req.Header.Set("X-Fride-Signature", nodeagent.SignFride(strings.TrimSpace(string(tok)), body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the webhook answered %d", resp.StatusCode)
	}
}
