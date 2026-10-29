package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// fakeHost is a node that offers fride_host, speaking the node protocol
// itself: its FrIDE instances are fakes that start and stop at once and
// count the memory they take, so the core's side can be tested before
// and apart from the node agent's.
type fakeHost struct {
	t    *testing.T
	ID   string
	name string

	wmu  sync.Mutex
	conn *websocket.Conn

	mu      sync.Mutex
	cap     nodeproto.FrideHostCap
	starts  []nodeproto.FrideInstanceStartParams
	calls   []string
	threads int
	// failStart makes a start fail with its text.
	failStart string
}

// fakeBundle is a task's export: a gzip'd tar with one file.
func fakeBundle() []byte {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	body := []byte("fake workspace\n")
	_ = tw.WriteHeader(&tar.Header{Name: "workspace/README.md", Mode: 0o600, Size: int64(len(body))})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()
	return b.Bytes()
}

// fakeHostNode joins a fake node offering fride_host with the memory
// given (MB) to the bridge's node port at addr, and approves its device
// and what it offers.
func (h *harness) fakeHostNode(name, addr string, totalMB, availableMB, reserveMB int) *fakeHost {
	h.t.Helper()
	t := h.t
	w := h.Do("POST", "/api/nodes/join-code", map[string]any{})
	var jc struct{ Code string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &jc) != nil {
		t.Fatalf("join-code: %d %s", w.Code, w.Body.String())
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeHost{t: t, ID: nodeproto.DeviceIDFor(pub), name: name,
		cap: nodeproto.FrideHostCap{Version: "sbx 0.0.0-test", Ready: true, TotalMemoryMB: totalMB, AvailableMemoryMB: availableMB, ReserveMB: reserveMB}}
	ctx, cancel := context.WithCancel(context.Background())
	conn, _, err := websocket.Dial(ctx, "ws://"+addr+nodeproto.Path, &websocket.DialOptions{Subprotocols: []string{nodeproto.Subprotocol}})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	conn.SetReadLimit(nodeproto.MaxMessageBytes)
	f.conn = conn
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); _ = conn.CloseNow(); <-done })
	hello := nodeproto.Hello{DeviceID: f.ID, PublicKey: pub, Name: name, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: "test",
		Capabilities: f.declared(), JoinCode: jc.Code}
	f.send(nodeproto.TypeHello, "", hello)
	for welcomed := false; !welcomed; {
		env := f.read(ctx)
		switch env.Type {
		case nodeproto.TypeChallenge:
			ch, err := nodeproto.DecodeBody[nodeproto.Challenge](env)
			if err != nil {
				t.Fatal(err)
			}
			f.send(nodeproto.TypeAuth, "", nodeproto.Auth{Signature: nodeproto.Sign(key, f.ID, ch.Nonce)})
		case nodeproto.TypeWelcome:
			welcomed = true
		default:
			t.Fatalf("%s during the handshake", env.Type)
		}
	}
	go func() {
		defer close(done)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			env, err := nodeproto.Decode(data)
			if err != nil || env.Type != nodeproto.TypeRequest {
				continue
			}
			req, err := nodeproto.DecodeBody[nodeproto.Request](env)
			if err != nil {
				continue
			}
			go func() {
				out, perr := f.handle(req)
				resp := nodeproto.Response{Error: perr}
				if perr == nil {
					resp.Result, _ = json.Marshal(out)
				}
				f.send(nodeproto.TypeResponse, env.ID, resp)
			}()
		}
	}()
	// Heard from, so the core takes it as online.
	f.send(nodeproto.TypeHeartbeat, "", nodeproto.Heartbeat{})
	for _, s := range []string{"approve", "approve-commands"} {
		if w := h.Do("POST", "/api/nodes/"+f.ID+"/"+s, nil); w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", s, w.Code, w.Body.String())
		}
	}
	h.WaitFor(name+" approved with fride_host", func() bool {
		n, err := h.b.nodes.Registry().Get(f.ID)
		return err == nil && h.b.nodes.Online(f.ID) && n.Effective().FrideHost != nil
	})
	return f
}

func (f *fakeHost) send(typ, id string, body any) {
	data, err := nodeproto.Encode(typ, id, body)
	if err != nil {
		f.t.Error(err)
		return
	}
	f.wmu.Lock()
	defer f.wmu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = f.conn.Write(ctx, websocket.MessageText, data)
}

func (f *fakeHost) read(ctx context.Context) nodeproto.Envelope {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, data, err := f.conn.Read(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	env, err := nodeproto.Decode(data)
	if err != nil {
		f.t.Fatal(err)
	}
	return env
}

// declared is what the node offers now.
func (f *fakeHost) declared() nodeproto.Capabilities {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.cap
	c.Instances = slices.Clone(f.cap.Instances)
	return nodeproto.Capabilities{Host: true, FrideHost: &c}
}

// declare tells the core what the node offers now, as the agent does
// right after an instance changes state.
func (f *fakeHost) declare() {
	f.send(nodeproto.TypeDeclare, "", nodeproto.Declare{Capabilities: f.declared()})
}

// setNoLogin sets why the node's instances' agents cannot log in to
// Claude ("" when they can), adds or changes the running instances in
// running (name → why its agents did not log in), and declares it.
func (f *fakeHost) setNoLogin(why string, running map[string]string) {
	f.mu.Lock()
	f.cap.NoLogin = why
	for name, no := range running {
		in := f.cap.Instance(name)
		if in == nil {
			f.cap.Instances = append(f.cap.Instances, nodeproto.FrideInstance{Name: name})
			in = &f.cap.Instances[len(f.cap.Instances)-1]
		}
		if in.State != nodeproto.FrideInstanceRunning {
			in.MemoryMB = 3072
			f.cap.AvailableMemoryMB -= in.MemoryMB
		}
		in.State, in.URL, in.NoLogin = nodeproto.FrideInstanceRunning, "http://127.0.0.1:17852", no
	}
	f.mu.Unlock()
	f.declare()
}

// emit sends FrIDE's news, as the node relays it.
func (f *fakeHost) emit(ev nodeproto.FrideEvent) {
	data, _ := json.Marshal(ev)
	f.send(nodeproto.TypeEvent, "", nodeproto.Event{Kind: nodeproto.EventFride, Data: data})
}

// Calls are the fride calls the node got, each as method@instance.
func (f *fakeHost) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// Starts are the fride.instance.start calls' params.
func (f *fakeHost) Starts() []nodeproto.FrideInstanceStartParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.starts)
}

// Instance is the instance called name, as the node has it.
func (f *fakeHost) Instance(name string) (nodeproto.FrideInstance, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if in := f.cap.Instance(name); in != nil {
		return *in, true
	}
	return nodeproto.FrideInstance{}, false
}

// Available is the memory the node reports free.
func (f *fakeHost) Available() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cap.AvailableMemoryMB
}

func (f *fakeHost) handle(req nodeproto.Request) (any, *nodeproto.Error) {
	var on nodeproto.FrideOn
	_ = json.Unmarshal(req.Params, &on)
	f.mu.Lock()
	f.calls = append(f.calls, req.Method+"@"+on.Instance)
	f.mu.Unlock()
	running := func(name string) *nodeproto.Error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if in := f.cap.Instance(name); in == nil || in.State != nodeproto.FrideInstanceRunning {
			return nodeproto.Errorf(nodeproto.CodeFailed, "the FrIDE instance %q does not run", name)
		}
		return nil
	}
	if strings.HasPrefix(req.Method, "fride.") && !slices.Contains(nodeproto.FrideHostMethods, req.Method) {
		if on.Instance == "" {
			return nil, nodeproto.Errorf(nodeproto.CodeDenied, "this node has no FrIDE of its own")
		}
		if perr := running(on.Instance); perr != nil {
			return nil, perr
		}
	}
	bundle := fakeBundle()
	switch req.Method {
	case nodeproto.MethodFrideInstanceStart:
		var p nodeproto.FrideInstanceStartParams
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Check() != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "start params: %v %v", err, p.Check())
		}
		f.mu.Lock()
		f.starts = append(f.starts, p)
		if f.failStart != "" {
			msg := f.failStart
			f.mu.Unlock()
			return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%s", msg)
		}
		in := f.cap.Instance(p.Name)
		if p.UpdateOnly && (in == nil || in.State != nodeproto.FrideInstanceRunning) {
			f.mu.Unlock()
			return nodeproto.FrideInstance{Name: p.Name, State: nodeproto.FrideInstanceStopped}, nil
		}
		if in == nil {
			f.cap.Instances = append(f.cap.Instances, nodeproto.FrideInstance{Name: p.Name, State: nodeproto.FrideInstanceStopped})
			in = &f.cap.Instances[len(f.cap.Instances)-1]
		}
		if in.State == nodeproto.FrideInstanceRunning {
			f.cap.AvailableMemoryMB += in.MemoryMB
		}
		f.cap.AvailableMemoryMB -= p.MemoryMB
		// Its agents log in when the node has the token.
		in.State, in.MemoryMB, in.URL, in.Problem, in.NoLogin = nodeproto.FrideInstanceRunning, p.MemoryMB, "http://127.0.0.1:17850", "", f.cap.NoLogin
		in.Repos = nil
		for _, r := range p.Repos {
			in.Repos = append(in.Repos, strings.TrimSuffix(r[strings.LastIndex(r, "/")+1:], ".git"))
		}
		out := *in
		f.mu.Unlock()
		f.declare()
		return out, nil
	case nodeproto.MethodFrideInstanceStop:
		var r nodeproto.FrideInstanceRef
		_ = json.Unmarshal(req.Params, &r)
		f.mu.Lock()
		in := f.cap.Instance(r.Name)
		if in == nil {
			f.mu.Unlock()
			return nil, nodeproto.Errorf(nodeproto.CodeNotFound, "no instance %q", r.Name)
		}
		if in.State == nodeproto.FrideInstanceRunning {
			f.cap.AvailableMemoryMB += in.MemoryMB
		}
		in.State, in.URL = nodeproto.FrideInstanceStopped, ""
		out := *in
		f.mu.Unlock()
		f.declare()
		return out, nil
	case nodeproto.MethodFrideInstanceStatus:
		return nodeproto.FrideInstances{Instances: f.declared().FrideHost.Instances}, nil
	case nodeproto.MethodFrideTask:
		f.mu.Lock()
		f.threads++
		n := f.threads
		f.mu.Unlock()
		return nodeproto.FrideThread{ThreadID: n, Status: "queued"}, nil
	case nodeproto.MethodFrideReply:
		var p nodeproto.FrideReplyParams
		_ = json.Unmarshal(req.Params, &p)
		return nodeproto.FrideThread{ThreadID: p.ThreadID, Status: "running"}, nil
	case nodeproto.MethodFrideStatus:
		var p nodeproto.FrideStatusParams
		_ = json.Unmarshal(req.Params, &p)
		return nodeproto.FrideStatus{Thread: nodeproto.FrideThread{ThreadID: p.ThreadID, Status: "running", TaskID: "login"}}, nil
	case nodeproto.MethodFrideTaskInfo:
		var p nodeproto.FrideTaskRef
		_ = json.Unmarshal(req.Params, &p)
		return nodeproto.FrideTask{TaskID: p.TaskID, Exists: true, Status: "in_progress"}, nil
	case nodeproto.MethodFrideClose:
		var p nodeproto.FrideTaskRef
		_ = json.Unmarshal(req.Params, &p)
		return nodeproto.FrideTask{TaskID: p.TaskID, Exists: true, Status: "closed", Branch: "fix/login", RationalePath: "/sandbox/ws/login/rationale.md"}, nil
	case nodeproto.MethodFrideExport:
		sum := sha256.Sum256(bundle)
		return nodeproto.FrideTransfer{ID: "t1", Size: int64(len(bundle)), SHA256: hex.EncodeToString(sum[:])}, nil
	case nodeproto.MethodFrideRead:
		var p nodeproto.FrideChunkParams
		_ = json.Unmarshal(req.Params, &p)
		return nodeproto.FrideChunk{Data: bundle[min(int(p.Offset), len(bundle)):], EOF: true}, nil
	case nodeproto.MethodFrideDrop, nodeproto.MethodFrideDelete:
		return struct{}{}, nil
	case nodeproto.MethodFrideRepoClone:
		var p nodeproto.FrideRepoParams
		_ = json.Unmarshal(req.Params, &p)
		return nodeproto.FrideRepo{Name: p.Name, Path: "/sandbox/repos/" + p.Name, Rescanned: true, Repos: []string{p.Name}}, nil
	case nodeproto.MethodFrideMem:
		return nodeproto.FrideMem{CanStart: true}, nil
	}
	return nil, nodeproto.Errorf(nodeproto.CodeUnsupported, "the fake node has no %s", req.Method)
}

// calledWith reports whether the node got method for instance.
func (f *fakeHost) calledWith(method, instance string) bool {
	return slices.Contains(f.Calls(), method+"@"+instance)
}

func (f *fakeHost) String() string { return fmt.Sprintf("%s (%s)", f.name, f.ID) }
