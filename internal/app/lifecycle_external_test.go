package app_test

import (
	"errors"
	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/app/apptest"
	"github.com/Icewater1337/fridolin/internal/platform"
	"net/http"
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
	lock, err := platform.AcquireLock(h.Config.Boot().DataDir)
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
