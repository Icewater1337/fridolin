package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/lease"
	"github.com/Icewater1337/fridolin/internal/tailnet"
)

// runningCore is a dev-mode bridge started with Run, as the command does,
// so that its tailnet node and lease run; its HTTP server listens on a
// free port of loopback.
type runningCore struct {
	*App
	home string
	stop func()
}

func newRunningCore(t *testing.T, extra string, opts Options) (*runningCore, error) {
	t.Helper()
	home := fsx.Canonical(t.TempDir())
	setenv(t, "HOME", home)
	path := writeConfig(t, `{"data_dir": "`+home+`/data", "claude_path": "`+fakeClaudeEnv(t, 10)+`",
		"allowed_numbers": ["15551234567"], "debounce": {"quiet_ms": 0}`+extra+`}`)
	return startCoreAt(t, home, path, opts)
}

func startCoreAt(t *testing.T, home, path string, opts Options) (*runningCore, error) {
	t.Helper()
	cfgs, err := openConfigStore(t, path)
	if err != nil {
		t.Fatal(err)
	}
	opts.Config, opts.NoWhatsApp = cfgs, true
	opts.Listen = func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
	b, err := New(opts)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = b.Run(ctx); close(done) }()
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	return &runningCore{App: b, home: home, stop: stop}, nil
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The tailnet listener serves the dashboard under the node's MagicDNS
// names only, always wants the dashboard token (here although
// dashboard.require_token is off), wants a token on /send, does not serve
// the brain API, and records who changed something in the audit log.
func TestTailnetServesTheDashboardWithTheToken(t *testing.T) {
	t.Parallel()
	fake := &tailnet.Fake{
		Status: tailnet.Status{DNSName: "fridolin-core.example.ts.net", IPs: []string{"100.64.0.1"}, MagicDNS: true},
		Who:    tailnet.Identity{LoginName: "you@example.com", Node: "laptop"},
	}
	c, err := newRunningCore(t, `, "dashboard": {"require_token": false}, "core": {"tailnet": {"enabled": true}}`,
		Options{Tailnet: tailnet.FakeFactory(fake), TailnetAuthKey: "tskey-test"})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the tailnet listener", func() bool { st := c.tailnetStatus(); return st != nil && st.State == "up" })
	addr := fake.Addr(":80")
	if addr == "" {
		t.Fatal("nothing listens on the tailnet's port 80")
	}
	if st := c.tailnetStatus(); st.URL != "http://fridolin-core.example.ts.net/" || st.Hostname != "fridolin-core" {
		t.Errorf("tailnet status = %+v", st)
	}
	do := func(method, host, path, body string, hdr map[string]string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}
	const host = "fridolin-core.example.ts.net"
	tok := strings.TrimSpace(readFile(t, filepath.Join(c.home, "data", "dashboard.token")))
	for _, tc := range []struct {
		method, host, path, body string
		hdr                      map[string]string
		want                     int
	}{
		{"GET", host, "/health", "", nil, 200},
		{"GET", "fridolin-core", "/health", "", nil, 200},
		{"GET", "127.0.0.1:8765", "/health", "", nil, http.StatusMisdirectedRequest},
		{"GET", "evil.example", "/health", "", nil, http.StatusMisdirectedRequest},
		{"GET", host, "/api/status", "", nil, http.StatusUnauthorized},
		{"GET", host, "/api/status", "", map[string]string{"X-Bridge-Token": tok}, 200},
		{"POST", host, "/send", `{"jid":"15551234567@s.whatsapp.net","text":"hi"}`, map[string]string{"Content-Type": "application/json"}, http.StatusUnauthorized},
		{"GET", host, "/brain/polls", "", nil, http.StatusNotFound},
		{"POST", host, "/api/logout", "{}", map[string]string{"X-Bridge-Token": tok, "X-Bridge-Dashboard": "1", "Content-Type": "application/json"}, 200},
	} {
		if got := do(tc.method, tc.host, tc.path, tc.body, tc.hdr); got.StatusCode != tc.want {
			t.Errorf("%s %s (Host %s): %d, want %d", tc.method, tc.path, tc.host, got.StatusCode, tc.want)
		}
	}
	audit := readFile(t, filepath.Join(c.home, "data", "audit", time.Now().Format("2006-01")+".jsonl"))
	if !strings.Contains(audit, `"kind":"tailnet","id":"laptop","by":"you@example.com","detail":"POST /api/logout","outcome":"200"`) {
		t.Errorf("audit log:\n%s", audit)
	}
	var state tailnetStateRecord
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(c.home, "data", "core", "tailnet.json"))), &state); err != nil || state.State != "up" || state.DNSName != host {
		t.Errorf("core/tailnet.json: %+v %v", state, err)
	}
	// The node port listens on the tailnet, at nodes.listen's port.
	ln, err := c.NodeListener("tcp", ":8766")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	if fake.Addr(":8766") == "" {
		t.Error("the node port does not listen on the tailnet")
	}
	c.stop()
	if _, err := fake.Listen("tcp", ":1"); !errors.Is(err, tailnet.ErrClosed) {
		t.Errorf("the node was not closed at stop: %v", err)
	}
}

// Without the tailnet, the node port binds as the HTTP server does.
func TestNodeListenerWithoutTailnet(t *testing.T) {
	t.Parallel()
	c, err := newRunningCore(t, "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := c.NodeListener("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}

// With core.tailnet on, Start opens the node port on the tailnet node,
// at nodes.listen's port, and not on this machine's interfaces; doctor's
// nodes-port check then reads the tailnet's state instead of dialing.
func TestNodesPortListensOnTheTailnet(t *testing.T) {
	t.Parallel()
	fake := &tailnet.Fake{
		Status: tailnet.Status{DNSName: "fridolin-core.example.ts.net.", IPs: []string{"100.64.0.1"}, MagicDNS: true},
		Who:    tailnet.Identity{LoginName: "you@example.com", Node: "laptop"},
	}
	c, err := newRunningCore(t, `, "nodes": {"enabled": true, "listen": "127.0.0.1:8766"}, "core": {"tailnet": {"enabled": true}}`,
		Options{Tailnet: tailnet.FakeFactory(fake), TailnetAuthKey: "tskey-test"})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the node port on the tailnet", func() bool { return fake.Addr(":8766") != "" && c.nodes.Addr() != "" })
	if got, want := c.nodes.Addr(), fake.Addr(":8766"); got != want {
		t.Errorf("node port at %q, want the tailnet's %q", got, want)
	}
	// Doctor reads the state the core writes to core/tailnet.json.
	waitUntil(t, "core/tailnet.json up", func() bool {
		var st tailnetStateRecord
		data, err := os.ReadFile(filepath.Join(c.home, "data", "core", "tailnet.json"))
		return err == nil && json.Unmarshal(data, &st) == nil && st.State == "up"
	})
	env := &DoctorEnv{Config: c.cfgs.Get(), ReadFile: os.ReadFile, Now: time.Now,
		Dial: func(context.Context, string) error { return errors.New("doctor dialed loopback") }}
	if r := checkNodesPort(context.Background(), env); r.Status != DoctorOK || !strings.Contains(r.Detail, "fridolin-core.example.ts.net:8766") {
		t.Errorf("nodes-port = %+v", r)
	}
}

const leaseConfig = `, "core": {"lease": {"enabled": true, "renew_seconds": 1, "stale_seconds": 4}}`

func readLease(t *testing.T, st cloud.ObjectStore) lease.Record {
	t.Helper()
	r, ok, err := lease.Read(context.Background(), st)
	if err != nil || !ok {
		t.Fatalf("lease: %v %v", ok, err)
	}
	return r
}

// The core takes the lease before its channels start, renews it, stops
// its channels and refuses to send once another device took it, keeps
// the dashboard up and says so, and refuses to start again afterwards.
func TestLeaseLostStopsTheChannels(t *testing.T) {
	t.Parallel()
	st := &cloud.Dir{Root: t.TempDir()}
	c, err := newRunningCore(t, leaseConfig, Options{LeaseStore: st, LeaseSettle: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	mine := readLease(t, st)
	if ls := c.leaseStatus(); ls == nil || ls.State != "held" || ls.DeviceID != mine.DeviceID {
		t.Fatalf("lease status = %+v, record %+v", ls, mine)
	}
	waitUntil(t, "a renewal", func() bool { return readLease(t, st).RenewedAt.After(mine.RenewedAt) })

	// Another device takes it over.
	other := lease.Record{DeviceID: "0123456789abcdef0123456789abcdef", Hostname: "core-b", StartedAt: time.Now().UTC(), RenewedAt: time.Now().UTC(), Version: "test"}
	data, _ := json.Marshal(other)
	if err := st.Put(context.Background(), lease.Key, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the lease to be lost", func() bool { ls := c.leaseStatus(); return ls != nil && ls.State == "lost" })
	if ls := c.leaseStatus(); !strings.Contains(ls.Holder, other.DeviceID) {
		t.Errorf("lost lease = %+v", ls)
	}
	if err := c.wa.SendMessage(context.Background(), "15551234567@s.whatsapp.net", "hi"); !errors.Is(err, errLeaseLost) {
		t.Errorf("a send after the loss: %v", err)
	}
	h := c.newHTTPHandler("127.0.0.1:8765")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1:8765/ready", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"state":"lost"`) {
		t.Errorf("/ready: %d %s", rec.Code, rec.Body)
	}
	if ws := c.coreWarnings(); len(ws) != 1 || !strings.Contains(ws[0], "core-b") {
		t.Errorf("warnings = %q", ws)
	}
	if got := readLease(t, st); got.DeviceID != other.DeviceID || got.Released {
		t.Errorf("the lost core wrote over the other's lease: %+v", got)
	}
	c.stop()
	if got := readLease(t, st); got.Released {
		t.Error("the lost core released the other's lease")
	}

	// This copy was taken over: it stays down, even once the other
	// releases the lease, until promote.
	other.Released = true
	data, _ = json.Marshal(other)
	if err := st.Put(context.Background(), lease.Key, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(filepath.Dir(c.cfgs.Path()), "config.json")
	if _, err := startCoreAt(t, c.home, cfg, Options{LeaseStore: st, LeaseSettle: time.Millisecond}); !errors.Is(err, ErrLeaseHeld) || !strings.Contains(err.Error(), "core promote") {
		t.Fatalf("restart of a copy taken over: %v", err)
	}
}

// A core that finds the lease held by another live device refuses to
// start; a clean stop releases it, so the next start takes it at once.
func TestLeaseRefusedAndReleased(t *testing.T) {
	t.Parallel()
	st := &cloud.Dir{Root: t.TempDir()}
	other := lease.Record{DeviceID: "0123456789abcdef0123456789abcdef", Hostname: "core-b", StartedAt: time.Now().UTC(), RenewedAt: time.Now().UTC()}
	data, _ := json.Marshal(other)
	if err := st.Put(context.Background(), lease.Key, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if _, err := newRunningCore(t, leaseConfig, Options{LeaseStore: st, LeaseSettle: time.Millisecond}); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("start under another's live lease: %v", err)
	}
	if _, err := newRunningCore(t, leaseConfig, Options{}); err == nil || !strings.Contains(err.Error(), "B2_VAULT_KEY_ID") {
		t.Fatalf("start without a store: %v", err)
	}

	st2 := &cloud.Dir{Root: t.TempDir()}
	c, err := newRunningCore(t, leaseConfig, Options{LeaseStore: st2, LeaseSettle: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	c.stop()
	if r := readLease(t, st2); !r.Released {
		t.Errorf("a clean stop left the lease %+v", r)
	}
	if ls := c.leaseStatus(); ls.State != "released" {
		t.Errorf("lease status after stop = %+v", ls)
	}
}
