package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// raceDetector is whether this test binary runs with the race detector
// (race_test.go). TestSmoke then builds the bridge with it as well, so
// that a data race in the bridge shows in its log.
var raceDetector bool

// TestSmoke builds the bridge and runs it as the service would, in dev mode
// on a scratch config, data dir and free port: it answers /health and the
// API, a brain answers a message, POST /api/restart brings the same process
// back serving, and SIGTERM stops it cleanly. -short skips it.
func TestSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the bridge")
	}
	root := canonicalDir(t.TempDir())
	home := filepath.Join(root, "home")
	data := filepath.Join(home, ".fridolin")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(root, "fridolin")
	args := []string{"build", "-o", bin}
	if raceDetector {
		args = append(args, "-race")
	}
	build := exec.Command("go", append(args, ".")...)
	build.Dir = repoDir
	// The go command's caches are under the HOME the tests started with.
	build.Env = append(os.Environ(), "HOME="+goToolHome)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// The fake Claude CLI is this test binary, whatever environment the
	// bridge gives the programs it runs. Quoted for sh, which would expand
	// a $ or a backtick in the path.
	claude := filepath.Join(root, "claude")
	script := fmt.Sprintf("#!/bin/sh\nFRIDOLIN_FAKE_CLAUDE=1 GORACE=atexit_sleep_ms=0 exec %s \"$@\"\n", shellQuote(os.Args[0]))
	if err := os.WriteFile(claude, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	cfg, err := json.Marshal(map[string]any{
		"data_dir":        data,
		"http_listen":     addr,
		"claude_path":     claude,
		"allowed_numbers": []string{normalizeJID(harnessDM)},
		"debounce":        map[string]int{"quiet_ms": 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(data, "config.json")
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(root, "bridge.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd := exec.Command(bin, "--no-whatsapp", "--config", cfgPath)
	cmd.Dir = root
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "TMPDIR=" + root}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-exited
		}
	}()
	fail := func(format string, args ...any) {
		t.Helper()
		log, _ := os.ReadFile(logPath)
		t.Fatalf(format+"\n--- the bridge's log:\n%s", append(args, log)...)
	}

	base := "http://" + addr
	client := &http.Client{Timeout: 5 * time.Second}
	get := func(path string, token string) (int, []byte) {
		req, _ := http.NewRequest("GET", base+path, nil)
		if token != "" {
			req.Header.Set("X-Bridge-Token", token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}
	post := func(path, token string, body any) (int, []byte) {
		data, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", base+path, bytes.NewReader(data))
		req.Header.Set("X-Bridge-Token", token)
		req.Header.Set("X-Bridge-Dashboard", "1")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return 0, []byte(err.Error())
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}
	waitUp := func(what string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if code, body := get("/health", ""); code == 200 && strings.TrimSpace(string(body)) == "ok" {
				return
			}
			select {
			case err := <-exited:
				stopped = true
				fail("the bridge exited (%v) before %s", err, what)
			case <-time.After(50 * time.Millisecond):
			}
		}
		fail("GET /health did not answer ok %s", what)
	}
	waitUp("starting")

	tokenData, err := os.ReadFile(filepath.Join(data, "dashboard.token"))
	if err != nil {
		fail("no dashboard token: %v", err)
	}
	token := strings.TrimSpace(string(tokenData))
	if code, _ := get("/api/status", ""); code != http.StatusUnauthorized {
		fail("GET /api/status without the token: %d, want 401", code)
	}
	startedAt := func() string {
		t.Helper()
		code, body := get("/api/status", token)
		var st struct {
			StartedAt string `json:"started_at"`
			DevMode   bool   `json:"dev_mode"`
		}
		if code != 200 || json.Unmarshal(body, &st) != nil || !st.DevMode {
			fail("GET /api/status: %d %s", code, body)
		}
		return st.StartedAt
	}
	first := startedAt()

	if code, body := post("/api/brains", token, map[string]any{
		"path": "~/main", "create_dir": true,
		"manifest": BrainManifest{ID: "main", Name: "Main", Routing: BrainRouting{HandlesDMs: true}},
	}); code != 200 {
		fail("add a brain: %d %s", code, body)
	}
	if code, body := post("/api/dev/inbound", token, inbound{JID: harnessDM, Text: "hello smoke"}); code != http.StatusAccepted {
		fail("dev inbound: %d %s", code, body)
	}
	answered := false
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && !answered; time.Sleep(50 * time.Millisecond) {
		_, body := get("/api/dev/outbox", token)
		answered = strings.Contains(string(body), "says: hello smoke")
	}
	if !answered {
		_, body := get("/api/dev/outbox", token)
		fail("no answer in the outbox: %s", body)
	}

	if code, body := post("/api/restart", token, nil); code != http.StatusAccepted {
		fail("POST /api/restart: %d %s", code, body)
	}
	// The same process, executed again: it stops serving, then serves anew.
	var second string
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if code, body := get("/api/status", token); code == 200 {
			var st struct {
				StartedAt string `json:"started_at"`
			}
			if json.Unmarshal(body, &st) == nil && st.StartedAt != first {
				second = st.StartedAt
				break
			}
		}
		select {
		case err := <-exited:
			stopped = true
			fail("the bridge exited (%v) on restart", err)
		default:
		}
	}
	if second == "" {
		fail("the bridge did not come back with a new started_at after POST /api/restart")
	}
	waitUp("after the restart")

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		stopped = true
		if err != nil {
			fail("on SIGTERM the bridge exited with %v, want 0", err)
		}
	case <-time.After(10 * time.Second):
		fail("the bridge did not stop within 10 s of SIGTERM")
	}
	if log, _ := os.ReadFile(logPath); bytes.Contains(log, []byte("panic:")) || bytes.Contains(log, []byte("DATA RACE")) {
		fail("a panic or data race in the log")
	}
}
