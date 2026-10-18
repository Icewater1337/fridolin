package main

import (
	"bufio"
	"context"
	"errors"
	"log"
	"net"
	"os"
	"strings"
	"sync"
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
