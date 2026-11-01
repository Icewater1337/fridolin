package brain

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

const testDMChat = "15551234567@s.whatsapp.net"
const testGroup = "120363000000000000@g.us"

func TestMain(m *testing.M) { testutil.Main(m) }
func dm(text string) Inbound {
	return Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: text}}
}
func inputRules() InputRules {
	return InputRules{
		Chats: func(m chat.Inbound) []string {
			ids := []string{m.JID}
			if m.ChatAlt != "" && m.ChatAlt != m.JID {
				ids = append(ids, m.ChatAlt)
			}
			return ids
		},
		Observe:   func(mode string, m chat.Inbound) bool { return mode == manifest.ObserveRun },
		BuildChat: func(jid string) bool { return strings.HasPrefix(jid, "build:") },
	}
}
func bareBrain(t testing.TB) *Brain {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return &Brain{id: "a", workDir: dir, root: root, input: inputRules(), lanes: newLanes()}
}

// testSink records runtime protocol events; user-facing formatting stays in
// the command adapter's tests.
type testSink struct {
	OnHistory func(string, history.Entry)
	Pending   func(string) []history.Entry
	Deliver   func(*Brain, Inbound, claudecli.Result)
	Ask       func(*Brain, string, string) bool
	Keep      func(*Brain, []Inbound)
}

func (s *testSink) Working(string)                      {}
func (s *testSink) Question(b *Brain, j, t string) bool { return s.Ask != nil && s.Ask(b, j, t) }
func (s *testSink) Finished(b *Brain, m Inbound, r claudecli.Result) {
	if s.Deliver != nil {
		s.Deliver(b, m, r)
	}
}
func (s *testSink) Waiting(b *Brain, ms []Inbound) {
	if s.Keep != nil {
		s.Keep(b, ms)
	} else {
		Dropped(ms)
	}
}
func (s *testSink) Interrupted(*Brain, Inbound) {}
func (s *testSink) Undelivered(id string) []history.Entry {
	if s.Pending != nil {
		return s.Pending(id)
	}
	return nil
}
func (s *testSink) History(id string, e history.Entry) {
	if s.OnHistory != nil {
		s.OnHistory(id, e)
	}
}
func sinkForTest(s *testSink) *brainSinkRef { return &brainSinkRef{target: s} }

type deliveries struct {
	mu   sync.Mutex
	runs []delivered
}
type delivered struct {
	brain string
	msg   Inbound
	res   claudecli.Result
}

func (d *deliveries) add(b *Brain, m Inbound, r claudecli.Result) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.runs = append(d.runs, delivered{b.ID(), m, r})
}
func (d *deliveries) count() int { d.mu.Lock(); defer d.mu.Unlock(); return len(d.runs) }
func (d *deliveries) all() []delivered {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]delivered(nil), d.runs...)
}
func queueTestManager(t *testing.T, sleepMS int, extra string) (*Manager, *deliveries, string) {
	t.Helper()
	if extra != "" {
		t.Fatalf("fixture must receive owned settings instead of configuration JSON: %s", extra)
	}
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", strconv.Itoa(sleepMS))
	home := t.TempDir()
	d := &deliveries{}
	runner := brainRunnerFunc(func(ctx context.Context, s RunSpec, c claudecli.Callbacks) claudecli.Result {
		env := []string{"FRIDOLIN_FAKE_CLAUDE=1", "GORACE=atexit_sleep_ms=0", "PATH=" + platform.SystemPATH, "HOME=" + home}
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "FRIDOLIN_FAKE_") {
				env = append(env, kv)
			}
		}
		s.BeforeStart()
		return claudecli.Run(ctx, claudecli.Invocation{Provider: s.Settings.Provider, Run: claudecli.Spawn{Binary: os.Args[0], Args: []string{"-p"}, Dir: s.Dir, Env: env, Prompt: s.Prompt, WaitDelay: runWaitDelay}, ExitGrace: postResultGrace, NoticeAfter: time.Hour}, c)
	})
	mgr := NewManager(Options{Runner: runner, Sink: &testSink{Deliver: d.add}, ClaimMedia: func(string, string, []string) map[string]string { return nil }, Input: inputRules(), ShutdownGrace: func() time.Duration { return 0 }})
	t.Cleanup(mgr.Stop)
	specs := []manifest.Spec{{Dir: filepath.Join(home, "a"), PromptFile: "brain-prompt.md", Manifest: manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: true}}}, {Dir: filepath.Join(home, "b"), Manifest: manifest.Manifest{ID: "b", Routing: manifest.Routing{Groups: []string{testGroup}}, Observe: manifest.ObserveRun}}}
	for _, s := range specs {
		if err := os.Mkdir(s.Dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if failures, err := mgr.Apply(specs, Globals{Defaults: Settings{QueueMode: "queue", MaxPending: 100, MediaDir: filepath.Join(home, "media")}}, ApplyOptions{}); err != nil || len(failures) > 0 {
		t.Fatalf("apply: %v %v", failures, err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_test"), []byte("FAKE-PRIVATE-KEY\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, sp := range specs {
		if err := os.WriteFile(filepath.Join(sp.Dir, "brain-prompt.md"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sp.Dir, manifest.File), []byte(`{"id":"`+sp.Manifest.ID+`"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return mgr, d, home
}

type logBuffer struct {
	mu    sync.Mutex
	lines []string
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}
func captureLog(t *testing.T) func() []string {
	t.Helper()
	b := &logBuffer{}
	old := log.Writer()
	log.SetOutput(b)
	t.Cleanup(func() { log.SetOutput(old) })
	return func() []string { b.mu.Lock(); defer b.mu.Unlock(); return append([]string(nil), b.lines...) }
}
func logContains(logged func() []string, s string) bool {
	for _, l := range logged() {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func folderBrain(t *testing.T) (*Brain, string) {
	m, _, home := queueTestManager(t, 0, "")
	if err := os.MkdirAll(filepath.Join(home, "a", "memories", "people"), 0700); err != nil {
		t.Fatal(err)
	}
	return m.Get("a"), home
}

const otherNumber = "15559876543"
const groupB = "120363000000000000@g.us"
const groupC = "120363000000000001@g.us"

func groupMsg(jid, text string, chatter bool) Inbound {
	return Inbound{Inbound: chat.Inbound{JID: jid, Sender: otherNumber, Text: text, Silent: chatter}}
}
func limitTestManager(t *testing.T, sleepMS, limit int) (*Manager, *deliveries) {
	t.Helper()
	mgr, d, home := queueTestManager(t, sleepMS, "")
	specs := []manifest.Spec{}
	for _, br := range mgr.Brains() {
		specs = append(specs, manifest.Spec{Dir: br.Dir(), Manifest: br.Manifest()})
	}
	dir := filepath.Join(home, "c")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	specs = append(specs, manifest.Spec{Dir: dir, Manifest: manifest.Manifest{ID: "c", Routing: manifest.Routing{Groups: []string{groupC}}, Observe: manifest.ObserveRun}})
	if f, e := mgr.Apply(specs, Globals{RunLimit: limit, Defaults: Settings{QueueMode: "queue", MaxPending: 100}}, ApplyOptions{}); e != nil || len(f) > 0 {
		t.Fatalf("apply:%v %v", f, e)
	}
	return mgr, d
}

func plant(t *testing.T, path, target string) {
	t.Helper()
	if e := os.Remove(path); e != nil && !os.IsNotExist(e) {
		t.Fatal(e)
	}
	if e := os.Symlink(target, path); e != nil {
		t.Fatal(e)
	}
}
func promptBlock(p, tag string) string {
	a, b := "<"+tag+">", "</"+tag+">"
	_, p, _ = strings.Cut(p, a)
	p, _, _ = strings.Cut(p, b)
	return strings.TrimSpace(p)
}
func viewerIdle(t *testing.T, br *Brain) {
	t.Helper()
	testutil.WaitFor(t, 20*time.Second, "the viewer's start", func() bool { br.viewerMu.Lock(); defer br.viewerMu.Unlock(); return !br.viewerStarting })
}

func setsidChildren(t *testing.T, home, holds string) {
	t.Helper()
	pidLog := filepath.Join(home, "pids")
	t.Setenv("FRIDOLIN_FAKE_PID_LOG", pidLog)
	t.Setenv("FRIDOLIN_FAKE_SETSID_CHILD", holds)
	t.Cleanup(func() {
		data, _ := os.ReadFile(pidLog)
		// Each run logs the CLI's PID, then its grandchild's.
		for i, line := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(line); err == nil && i%2 == 1 {
				testutil.KillPID(pid)
			}
		}
	})
}

var postResultGrace = 5 * time.Second
var runWaitDelay = 250 * time.Millisecond

func readHistory(t *testing.T, br *Brain) []history.Entry {
	t.Helper()
	data, e := os.ReadFile(filepath.Join(br.Dir(), history.FileName))
	if e != nil && !os.IsNotExist(e) {
		t.Fatal(e)
	}
	var out []history.Entry
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if l == "" {
			continue
		}
		var v history.Entry
		if e := json.Unmarshal([]byte(l), &v); e != nil {
			t.Fatal(e)
		}
		out = append(out, v)
	}
	return out
}
