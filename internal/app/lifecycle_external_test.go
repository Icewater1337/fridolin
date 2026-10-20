package app_test

import (
	"context"
	"errors"
	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/app/apptest"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/platform"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestApplicationRestartStopsAndReleasesInstanceLock(t *testing.T) {
	h := apptest.New(t)
	if status, body := h.Do(http.MethodPost, "/api/restart", nil); status != http.StatusAccepted {
		t.Fatalf("restart: %d %s", status, body)
	}
	select {
	case <-h.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("restart did not finish")
	}
	if !errors.Is(h.Err(), app.ErrRestart) {
		t.Fatalf("restart cause: %v", h.Err())
	}
	lock, err := platform.AcquireLock(config.Layout{Root: h.Config.Boot().DataDir}.Lock())
	if err != nil {
		t.Fatalf("restart retained instance lock: %v", err)
	}
	lock.Release()
}

func TestApplicationCancellationStopsNormally(t *testing.T) {
	h := apptest.New(t)
	if status, body := h.Do(http.MethodGet, "/api/status", nil); status != http.StatusOK {
		t.Fatalf("status: %d %s", status, body)
	}
	h.Close()
	if err := h.Err(); err != nil {
		t.Fatalf("cancel: %v", err)
	}
}

func TestApplicationStopOwnsRunShutdown(t *testing.T) {
	home := t.TempDir()
	cfg, err := config.Open(filepath.Join(home, "config.json"), config.Options{GOOS: runtime.GOOS, Paths: config.Paths{Home: home, DefaultDataDir: filepath.Join(home, "data")}, Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	a, err := app.New(app.Options{Config: cfg, NoWhatsApp: true, Listen: func(string, string) (net.Listener, error) {
		close(entered)
		<-release
		return net.Listen("tcp", "127.0.0.1:0")
	}})
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(context.Background()) }()
	<-entered
	stopped := make(chan struct{})
	go func() { a.Stop(); close(stopped) }()
	close(release)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not finish")
	}
	if err := <-runDone; err != nil {
		t.Fatalf("Run after Stop: %v", err)
	}
	lock, err := platform.AcquireLock(config.Layout{Root: cfg.Boot().DataDir}.Lock())
	if err != nil {
		t.Fatalf("Stop retained instance lock: %v", err)
	}
	lock.Release()
	if err := a.Run(context.Background()); err == nil {
		t.Fatal("stopped application ran again")
	}
}

func TestApplicationStopBeforeRunReleasesConstruction(t *testing.T) {
	home := t.TempDir()
	cfg, err := config.Open(filepath.Join(home, "config.json"), config.Options{GOOS: runtime.GOOS, Paths: config.Paths{Home: home, DefaultDataDir: filepath.Join(home, "data")}, Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(app.Options{Config: cfg, NoWhatsApp: true})
	if err != nil {
		t.Fatal(err)
	}
	a.Stop()
	if err := a.Run(context.Background()); err == nil {
		t.Fatal("application started after resources closed")
	}
	lock, err := platform.AcquireLock(config.Layout{Root: cfg.Boot().DataDir}.Lock())
	if err != nil {
		t.Fatalf("Stop retained instance lock: %v", err)
	}
	lock.Release()
}
