package testutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// FakeFride is a stand-in for FrIDE in companion mode
// (its docs/COMPANION.md): its
// loopback API under /api/companion/, the DELETE /api/tasks/<task> of its
// UI's API, and its signed webhook. Tests drive what FrIDE's orchestrator
// would do with Emit, Ask, Start and Finish.
type FakeFride struct {
	t   testing.TB
	srv *http.Server
	ln  net.Listener
	URL string

	mu        sync.Mutex
	code      string
	tokenHash [32]byte
	paired    bool
	webhook   string
	repos     []string
	canStart  bool
	threads   map[int]*fakeThread
	tasks     map[string]*fakeTask
	nextID    int
	// Calls records "METHOD path" of every request, in order.
	calls []string
}

type fakeThread struct {
	ID       int    `json:"thread_id"`
	Status   string `json:"status"`
	TaskID   string `json:"task_id"`
	Question string `json:"question"`
	Message  string `json:"-"`
	Ref      string `json:"-"`
	Replies  []string
}

type fakeTask struct {
	ID        string `json:"task_id"`
	Exists    bool   `json:"exists"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Branch    string `json:"branch"`
	Rationale string `json:"rationale_path,omitempty"`
	Source    string `json:"source_task_id,omitempty"`
	Content   string `json:"-"`
}

// StartFakeFride serves a FakeFride on a loopback port until the test
// ends, knowing repos.
func StartFakeFride(t testing.TB, repos ...string) *FakeFride {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &FakeFride{t: t, ln: ln, URL: "http://" + ln.Addr().String(), repos: repos, canStart: true,
		threads: map[int]*fakeThread{}, tasks: map[string]*fakeTask{}}
	f.srv = &http.Server{Handler: http.HandlerFunc(f.serve)}
	go func() { _ = f.srv.Serve(ln) }()
	t.Cleanup(f.Stop)
	return f
}

// Stop stops answering, as a FrIDE that was quit.
func (f *FakeFride) Stop() { _ = f.srv.Close() }

// NewCode is what `fride pair-companion` prints: a code valid once.
func (f *FakeFride) NewCode() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.code = fmt.Sprintf("FAKE-%04d", len(f.calls))
	return f.code
}

// SetCanStart sets what /api/companion/mem answers.
func (f *FakeFride) SetCanStart(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canStart = v
}

// Calls lists "METHOD path" of every request so far.
func (f *FakeFride) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Webhook is where FrIDE sends its news, "" before pairing.
func (f *FakeFride) Webhook() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.webhook
}

// Task reports a task: whether it exists, and its status.
func (f *FakeFride) Task(id string) (exists bool, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t := f.tasks[id]; t != nil {
		return t.Exists, t.Status
	}
	return false, ""
}

// Replies are the answers a thread got.
func (f *FakeFride) Replies(thread int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if th := f.threads[thread]; th != nil {
		return append([]string(nil), th.Replies...)
	}
	return nil
}

// Emit posts event (FrIDE's webhook body) to the webhook, signed as FrIDE
// signs it, and returns the HTTP status it got.
func (f *FakeFride) Emit(event map[string]any) int {
	f.mu.Lock()
	url, key := f.webhook, f.tokenHash
	f.mu.Unlock()
	body, _ := json.Marshal(event)
	m := hmac.New(sha256.New, key[:])
	m.Write(body)
	return f.post(url, body, "sha256="+hex.EncodeToString(m.Sum(nil)))
}

// EmitUnsigned posts event with a wrong signature.
func (f *FakeFride) EmitUnsigned(event map[string]any) int {
	body, _ := json.Marshal(event)
	return f.post(f.Webhook(), body, "sha256=00")
}

func (f *FakeFride) post(url string, body []byte, sig string) int {
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Fride-Signature", sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Logf("fake FrIDE: posting the webhook: %v", err)
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// Ask makes thread ask its sender question, and says so on the webhook.
func (f *FakeFride) Ask(thread int, question string) int {
	f.mu.Lock()
	th := f.threads[thread]
	th.Status, th.Question = "needs_reply", question
	f.mu.Unlock()
	return f.Emit(map[string]any{"event": "question", "thread_id": thread, "task_id": th.TaskID, "text": fmt.Sprintf("FrIDE inbox #%d · needs your reply\n%s", thread, question), "status": "needs_reply"})
}

// Start hands thread to a new task on branch, and says so.
func (f *FakeFride) Start(thread int, task, branch string) int {
	f.mu.Lock()
	th := f.threads[thread]
	th.Status, th.TaskID, th.Question = "active", task, ""
	f.tasks[task] = &fakeTask{ID: task, Exists: true, Name: task, Status: "open", Branch: branch, Content: "work of " + task}
	f.mu.Unlock()
	return f.Emit(map[string]any{"event": "started", "thread_id": thread, "task_id": task, "text": fmt.Sprintf("FrIDE inbox #%d · following · %s", thread, task), "status": "active", "branch": branch})
}

func (f *FakeFride) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(code int, msg string) { reply(code, map[string]string{"error": msg}) }
	path := r.URL.Path
	if path == "/api/companion/pair" && r.Method == http.MethodPost {
		var p struct {
			Code    string `json:"code"`
			Webhook string `json:"webhook_url"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		if f.code == "" || p.Code != f.code {
			fail(http.StatusForbidden, "wrong or used code")
			return
		}
		f.code = ""
		token := "frc_fake" + strconv.Itoa(len(f.calls))
		f.tokenHash, f.paired, f.webhook = sha256.Sum256([]byte(token)), true, p.Webhook
		reply(http.StatusOK, map[string]string{"token": token, "webhook_url": p.Webhook})
		return
	}
	if r.Method == http.MethodDelete && strings.HasPrefix(path, "/api/tasks/") {
		id := strings.TrimPrefix(path, "/api/tasks/")
		if t := f.tasks[id]; t != nil {
			t.Exists = false
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !strings.HasPrefix(path, "/api/companion/") {
		fail(http.StatusNotFound, "not found")
		return
	}
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !f.paired || sha256.Sum256([]byte(auth)) != f.tokenHash {
		fail(http.StatusUnauthorized, "no or wrong token")
		return
	}
	rest := strings.TrimPrefix(path, "/api/companion/")
	switch {
	case rest == "info":
		reply(http.StatusOK, map[string]any{"mode": "companion", "configured": true, "repos": f.repos, "profiles": []string{"local"}, "webhook_url": f.webhook, "orchestrator": true})
	case rest == "mem":
		reason := ""
		if !f.canStart {
			reason = "not enough memory for another stack"
		}
		reply(http.StatusOK, map[string]any{"can_start": f.canStart, "reason": reason, "running_stacks": 0, "mem": map[string]any{"hostFree": 8 << 30}})
	case rest == "tasks" && r.Method == http.MethodPost:
		var p struct {
			Message string   `json:"message"`
			Repos   []string `json:"repos"`
			Ref     string   `json:"ref"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		for _, th := range f.threads {
			if p.Ref != "" && th.Ref == p.Ref {
				reply(http.StatusOK, map[string]any{"thread_id": th.ID, "status": th.Status, "task_id": th.TaskID, "existing": true})
				return
			}
		}
		f.nextID++
		th := &fakeThread{ID: f.nextID, Status: "queued", Message: p.Message, Ref: p.Ref}
		// The orchestrator sends a message that names a task to it.
		for id, t := range f.tasks {
			if t.Exists && strings.Contains(p.Message, "task "+id) {
				th.TaskID, th.Status = id, "active"
			}
		}
		f.threads[th.ID] = th
		reply(http.StatusAccepted, map[string]any{"thread_id": th.ID, "status": th.Status, "task_id": "", "existing": false})
	case strings.HasPrefix(rest, "threads/"):
		parts := strings.Split(strings.TrimPrefix(rest, "threads/"), "/")
		id, _ := strconv.Atoi(parts[0])
		th := f.threads[id]
		if th == nil {
			fail(http.StatusNotFound, "no such thread")
			return
		}
		if len(parts) == 2 && parts[1] == "reply" && r.Method == http.MethodPost {
			var p struct {
				Text string `json:"text"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			th.Replies = append(th.Replies, p.Text)
			th.Status, th.Question = "queued", ""
			reply(http.StatusAccepted, th)
			return
		}
		reply(http.StatusOK, th)
	case rest == "tasks/import" && r.Method == http.MethodPost:
		id, content, err := readFakeBundle(r.Body)
		if err != nil {
			fail(http.StatusBadRequest, err.Error())
			return
		}
		src := id
		if as := r.URL.Query().Get("as"); as != "" {
			id = as
		}
		if t := f.tasks[id]; t != nil && t.Exists {
			fail(http.StatusConflict, "a task "+id+" exists here")
			return
		}
		t := &fakeTask{ID: id, Exists: true, Name: id, Status: "in_progress", Branch: "feat/" + src, Source: src, Content: content}
		f.tasks[id] = t
		reply(http.StatusCreated, t)
	case strings.HasPrefix(rest, "tasks/"):
		parts := strings.Split(strings.TrimPrefix(rest, "tasks/"), "/")
		t := f.tasks[parts[0]]
		if t == nil || !t.Exists {
			fail(http.StatusNotFound, "no such task")
			return
		}
		switch {
		case len(parts) == 1:
			reply(http.StatusOK, t)
		case parts[1] == "close" && r.Method == http.MethodPost:
			t.Status, t.Rationale = "done", "/fake/ws/"+t.ID+"/docs/rationale/"+t.ID+".md"
			reply(http.StatusOK, t)
			go f.Emit(map[string]any{"event": "done", "thread_id": f.threadOf(t.ID), "task_id": t.ID, "text": "FrIDE inbox · done · " + t.ID, "status": "done", "branch": t.Branch, "rationale_path": t.Rationale})
		case parts[1] == "export":
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(fakeBundle(t.ID, t.Content))
		default:
			fail(http.StatusNotFound, "not found")
		}
	default:
		fail(http.StatusNotFound, "not found")
	}
}

// threadOf is the thread following task, 0 for none; called with mu held.
func (f *FakeFride) threadOf(task string) int {
	for _, th := range f.threads {
		if th.TaskID == task {
			return th.ID
		}
	}
	return 0
}

// fakeBundle is a gzip'd tar holding the task's id and content, as FrIDE's
// export is a gzip'd tar.
func fakeBundle(id, content string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"task.id": id, "work.txt": content} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func readFakeBundle(r io.Reader) (id, content string, err error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", "", err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", err
		}
		data, _ := io.ReadAll(tr)
		switch h.Name {
		case "task.id":
			id = string(data)
		case "work.txt":
			content = string(data)
		}
	}
	if id == "" {
		return "", "", fmt.Errorf("not a bundle")
	}
	return id, content, nil
}
