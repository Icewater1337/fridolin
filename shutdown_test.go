package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// The bridge's stop, as SIGTERM and a restart make it.

// Stop runs once, however often and from wherever it is called: a restart
// and a signal that come together, or a test and its cleanup.
func TestStopRunsOnce(t *testing.T) {
	tb := newTestBridge(t, "")
	var logs lockedBuffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&logs)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(tb.Stop)
	}
	wg.Wait()
	tb.Stop()
	if n := strings.Count(logs.String(), "Bridge stopped."); n != 1 {
		t.Errorf("the bridge stopped %d times, want once:\n%s", n, logs.String())
	}
	lock, err := acquireInstanceLock(tb.cfgs.Get().DataDir)
	if err != nil {
		t.Fatalf("the data dir is still locked after Stop: %v", err)
	}
	lock.release()
}

// brokenListener is a listener that has stopped taking connections, as
// one does whose socket an outside event closed.
type brokenListener struct{ net.Listener }

var errListenerBroke = errors.New("the listener broke")

func (brokenListener) Accept() (net.Conn, error) { return nil, errListenerBroke }

// A server that fails after it was bound leaves /send and the dashboard
// dead: Start returns the error, for main to stop the bridge, instead of
// running on without them.
func TestStartReturnsAServeError(t *testing.T) {
	tb := newTestBridge(t, `, "http_listen": "127.0.0.1:0"`)
	prev := listenHTTP
	t.Cleanup(func() { listenHTTP = prev })
	listenHTTP = func(network, addr string) (net.Listener, error) {
		ln, err := prev(network, addr)
		return brokenListener{ln}, err
	}
	done := make(chan error, 1)
	go func() { done <- tb.Start(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, errListenerBroke) {
			t.Errorf("Start returned %v, want the server's error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return when its HTTP server failed")
	}
}

// A request still running when the bridge stops ends with it: its context
// is done once the stop begins, and what is left of it after httpStopWait
// is cut off, so that it cannot hold up the stop.
func TestStopEndsTheRequestsInFlight(t *testing.T) {
	tb := newTestBridge(t, `, "http_listen": "127.0.0.1:0"`)
	prevWait, prevListen := httpStopWait, listenHTTP
	t.Cleanup(func() { httpStopWait, listenHTTP = prevWait, prevListen })
	httpStopWait = 200 * time.Millisecond
	addr := make(chan string, 1)
	listenHTTP = func(network, a string) (net.Listener, error) {
		ln, err := prevListen(network, a)
		if err == nil {
			addr <- ln.Addr().String()
		}
		return ln, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tb.Start(ctx) }()
	// A request whose body never comes in full: its handler waits for the
	// rest for ReadTimeout, thirty seconds. The server says 100 Continue
	// once the handler reads it.
	conn, err := net.Dial("tcp", <-addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	req := "POST /api/dev/inbound HTTP/1.1\r\nHost: 127.0.0.1:0\r\nX-Bridge-Token: " + tb.token.Value() +
		"\r\nX-Bridge-Dashboard: 1\r\nContent-Type: application/json\r\nContent-Length: 100\r\nExpect: 100-continue\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if !strings.Contains(line, "100 Continue") {
		t.Fatalf("the server answered %q (%v), want 100 Continue", line, err)
	}
	if _, err := conn.Write([]byte(`{"jid": `)); err != nil {
		t.Fatal(err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start: %v", err)
	}
	if tb.httpSrv.BaseContext == nil || tb.httpSrv.BaseContext(nil).Err() == nil {
		t.Error("the requests' context goes on after the bridge began to stop")
	}
	stopped := time.Now()
	tb.Stop()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("the request was still connected %v after Stop began", time.Since(stopped).Round(time.Second))
	}
}

// errExecStub is what the stand-in for execSelf returns: exec returns
// only when it fails.
var errExecStub = errors.New("exec: not in a test")

// serve stops the bridge once however it ends, and runs it again only for
// a restart that no signal overtook, once the instance lock is free for
// the process that takes over.
func TestServe(t *testing.T) {
	tests := []struct {
		name      string
		end       func(t *testing.T, tb *testBridge, cancel context.CancelCauseFunc)
		signalled bool
		broken    bool // Start's HTTP server fails
		code      int
		execs     int
	}{
		{"restart", func(t *testing.T, tb *testBridge, _ context.CancelCauseFunc) {
			if w := tb.do("POST", "/api/restart", nil); w.Code != 202 {
				t.Errorf("POST /api/restart: %d %s", w.Code, w.Body.String())
			}
		}, false, false, 1, 1},
		{"signal during a restart", func(_ *testing.T, tb *testBridge, _ context.CancelCauseFunc) {
			tb.do("POST", "/api/restart", nil)
		}, true, false, 0, 0},
		{"signal", func(_ *testing.T, _ *testBridge, cancel context.CancelCauseFunc) {
			cancel(errors.New("received signal terminated"))
		}, true, false, 0, 0},
		{"server fails", func(*testing.T, *testBridge, context.CancelCauseFunc) {}, false, true, 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tb := newTestBridge(t, `, "http_listen": "127.0.0.1:0"`)
			dataDir := tb.cfgs.Get().DataDir
			var logs lockedBuffer
			defer log.SetOutput(log.Writer())
			log.SetOutput(&logs)
			prevExec, prevListen := execSelf, listenHTTP
			t.Cleanup(func() { execSelf, listenHTTP = prevExec, prevListen })
			if tc.broken {
				listenHTTP = func(network, addr string) (net.Listener, error) {
					ln, err := prevListen(network, addr)
					return brokenListener{ln}, err
				}
			}
			var execs []string
			var lockErr error
			execSelf = func(argv0 string, argv, env []string) error {
				execs = append(execs, argv0)
				// What the new process's NewBridge takes first.
				lock, err := acquireInstanceLock(dataDir)
				if lockErr = err; err == nil {
					lock.release()
				}
				return errExecStub
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			tb.restart = func() { cancel(errRestart) }
			code := make(chan int, 1)
			go func() { code <- serve(ctx, tb.Bridge, func() bool { return tc.signalled }) }()
			tc.end(t, tb, cancel)
			select {
			case got := <-code:
				if got != tc.code {
					t.Errorf("exit code %d, want %d", got, tc.code)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("serve did not return")
			}
			if len(execs) != tc.execs {
				t.Errorf("executed %v, want %d run", execs, tc.execs)
			}
			if exe, _ := os.Executable(); len(execs) > 0 && execs[0] != exe {
				t.Errorf("executed %s, want this binary %s", execs[0], exe)
			}
			if lockErr != nil {
				t.Errorf("the instance lock was still held when the bridge ran again: %v", lockErr)
			}
			if n := strings.Count(logs.String(), "Bridge stopped."); n != 1 {
				t.Errorf("the bridge stopped %d times, want once:\n%s", n, logs.String())
			}
		})
	}
}

// Without a way to restart, as in check or a test, the restart button
// says so instead of answering that it restarts.
func TestRestartNeedsSomeoneToRestart(t *testing.T) {
	tb := newTestBridge(t, "")
	if w := tb.do("POST", "/api/restart", nil); w.Code != http.StatusServiceUnavailable {
		t.Errorf("POST /api/restart: %d %s, want 503", w.Code, w.Body.String())
	}
}

// A second SIGTERM while the bridge stops, here giving a brain's run its
// grace to finish, stops the run and the bridge at once, with exit code 1.
// The bridge runs as its own process, the test binary as main, with the
// grace a bridge has outside the tests.
func TestASecondSignalStopsAtOnce(t *testing.T) {
	addr := freePort(t)
	root, cfg := cliFixture(t, `"http_listen": "`+addr+`", "debounce": {"quiet_ms": 0}`, func(home string) []BrainRef {
		return []BrainRef{brainFolder(t, "~/main", BrainManifest{ID: "main", Name: "Main", Routing: BrainRouting{HandlesDMs: true}})}
	})
	home := filepath.Join(root, "home")
	cmd := exec.Command(filepath.Join(root, "bin", "fridolin"), "--no-whatsapp", "--config", cfg)
	cmd.Dir = root
	cmd.Env = []string{
		"FRIDOLIN_TEST_RUN_MAIN=1", "FRIDOLIN_FAKE_CLAUDE=1", "FRIDOLIN_FAKE_SLEEP_MS=60000",
		"HOME=" + home, "PATH=/usr/bin:/bin", "GORACE=atexit_sleep_ms=0", "GOCOVERDIR=" + t.TempDir(),
	}
	var out lockedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})

	client := &http.Client{Timeout: 5 * time.Second}
	call := func(method, path, token string, body string) (int, string) {
		req, _ := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("X-Bridge-Token", token)
			req.Header.Set("X-Bridge-Dashboard", "1")
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(data)
	}
	waitFor(t, 20*time.Second, "the bridge to serve", func() bool { code, _ := call("GET", "/health", "", ""); return code == 200 })
	data, err := os.ReadFile(filepath.Join(home, ".fridolin", "dashboard.token"))
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(data))
	if code, body := call("POST", "/api/dev/inbound", token, `{"jid": "`+harnessDM+`", "text": "take your time"}`); code != http.StatusAccepted {
		t.Fatalf("dev inbound: %d %s", code, body)
	}
	var pid int
	waitFor(t, 20*time.Second, "the run to start", func() bool {
		_, body := call("GET", "/api/status", token, "")
		var st struct {
			Brains []struct {
				Running *RunView `json:"running"`
			} `json:"brains"`
		}
		if json.Unmarshal([]byte(body), &st) == nil && len(st.Brains) == 1 && st.Brains[0].Running != nil {
			pid = st.Brains[0].Running.PID
		}
		return pid > 0
	})

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the stop to begin", func() bool { return strings.Contains(out.String(), "shutting down") })
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-exited:
		exited <- err
		t.Fatalf("the bridge exited (%v) before its run was over or its grace was:\n%s", err, out.String())
	default:
	}
	sent := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		exited <- err
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Errorf("after the second signal the bridge exited with %v, want exit status 1:\n%s", err, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the bridge was still stopping 5s after the second signal:\n%s", out.String())
	}
	t.Logf("exited %v after the second signal", time.Since(sent).Round(time.Millisecond))
	// The run's process group is gone, though its 60 seconds are not.
	waitFor(t, 5*time.Second, "the run to be killed", func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH })
}

// setShutdownGrace gives the runs in flight d to finish when the test's
// bridges stop.
func setShutdownGrace(t *testing.T, d time.Duration) {
	t.Helper()
	prev := shutdownGrace
	shutdownGrace = d
	t.Cleanup(func() { shutdownGrace = prev })
}

// The run in flight when the bridge stops gets the grace to finish: one
// that does is delivered, its reply in the outbox before the store closes
// and sent once, before the stop or after the next start. One that does
// not is stopped once the grace is over, and the stop goes on.
func TestStopGivesTheRunInFlightItsGrace(t *testing.T) {
	tests := []struct {
		name    string
		runMS   string
		grace   time.Duration
		replies int // how often the reply goes out
	}{
		{"finishes within it", "1500", 20 * time.Second, 1},
		{"outlasts it", "30000", 300 * time.Millisecond, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setShutdownGrace(t, tc.grace)
			h := newHarness(t, "")
			dir := h.AddBrain(BrainManifest{ID: "main", Name: "Main", Routing: BrainRouting{HandlesDMs: true}})
			t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", tc.runMS)
			h.Inject(inbound{JID: harnessDM, Text: "almost done"})
			h.WaitFor("the run", func() bool { return h.Running("main") })
			began := time.Now()
			h.Shutdown()
			if took := time.Since(began); took > tc.grace+stopRunWait {
				t.Errorf("the stop took %v, more than the grace and the wait for the stopped run", took)
			}
			h.Reopen()
			count := func() (replies, lines int) {
				for _, text := range h.SentTo(harnessDM) {
					if strings.Contains(text, "says: almost done") {
						replies++
					}
				}
				for _, l := range historyLines(t, dir) {
					if strings.Contains(l, "says: almost done") {
						lines++
					}
				}
				return replies, lines
			}
			// The history records the reply once it went out.
			h.WaitFor("the reply in the history", func() bool { _, lines := count(); return lines >= tc.replies })
			time.Sleep(500 * time.Millisecond)
			if replies, lines := count(); replies != tc.replies || lines != tc.replies {
				t.Errorf("the reply went out %d times and is in the history %d times, want %d", replies, lines, tc.replies)
			}
		})
	}
}

// A stop waits for the run's delivery too, not only for the run: the
// bridge closes the outbox and the store once the brains have stopped, and
// a reply handed over after that would be lost.
func TestStopWaitsForTheDelivery(t *testing.T) {
	setShutdownGrace(t, 10*time.Second)
	mgr, _, _ := queueTestManager(t, 300, "")
	var delivered atomic.Bool
	mgr.SetHooks(BrainHooks{Deliver: func(br *Brain, m Inbound, res RunResult) {
		time.Sleep(500 * time.Millisecond) // the outbox's write, slowed down
		delivered.Store(true)
	}})
	if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
		t.Fatal(err)
	}
	hasRun(t, mgr.Get("a"))
	mgr.Stop()
	if !delivered.Load() {
		t.Error("Stop returned before the run's reply was delivered")
	}
}
