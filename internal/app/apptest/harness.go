// Package apptest runs an application under an isolated home and ephemeral
// listener. Tests interact with the guarded HTTP API and lifecycle boundary.
package apptest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

type Harness struct {
	t                    testing.TB
	Home                 string
	Config               *config.Store
	client               *http.Client
	address, host, token string
	cancel               context.CancelFunc
	done                 chan struct{}
	err                  error
}

// New starts the real application in dev mode. Configuration paths and the
// listener are supplied explicitly; no live data or WhatsApp session is used.
func New(t testing.TB) *Harness {
	t.Helper()
	home := t.TempDir()
	cfg, err := config.Open(filepath.Join(home, "config.json"), config.Options{GOOS: runtime.GOOS, Paths: config.Paths{Home: home, DefaultDataDir: filepath.Join(home, "data")}, Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	bound := make(chan net.Listener, 1)
	a, err := app.New(app.Options{Config: cfg, NoWhatsApp: true, Listen: func(string, string) (net.Listener, error) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			bound <- ln
		}
		return ln, err
	}})
	if err != nil {
		t.Fatal(err)
	}
	token, err := httpapi.ReadToken(cfg.Boot().DataDir)
	if err != nil {
		a.Stop()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &Harness{t: t, Home: home, Config: cfg, host: cfg.Boot().HTTPListen, token: token, cancel: cancel, done: make(chan struct{}), client: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}}
	go func() { h.err = a.Run(ctx); close(h.done) }()
	t.Cleanup(h.Close)
	select {
	case ln := <-bound:
		h.address = "http://" + ln.Addr().String()
	case <-h.done:
		t.Fatalf("application ended before listening: %v", h.err)
	case <-time.After(10 * time.Second):
		t.Fatal("application did not listen")
	}
	return h
}

// Do sends a dashboard request through the real server and returns its status
// and complete body. Credentials stay in this scratch harness.
func (h *Harness) Do(method, path string, body any) (int, []byte) {
	h.t.Helper()
	var input io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		input = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, h.address+path, input)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Host = h.host
	req.Header.Set("X-Bridge-Token", h.token)
	if method != http.MethodGet {
		req.Header.Set("X-Bridge-Dashboard", "1")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp.StatusCode, data
}

func (h *Harness) Done() <-chan struct{} { return h.done }
func (h *Harness) Err() error            { <-h.done; return h.err }
func (h *Harness) Close() {
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		h.t.Error("application did not stop within 10s")
	}
	h.client.CloseIdleConnections()
}
