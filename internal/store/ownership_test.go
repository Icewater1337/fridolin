package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDeviceAndApplicationTablesSharePool(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	s.db.SetMaxOpenConns(1)
	conn, err := s.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Holding the sole application connection must also prevent the device
	// container from opening a second, independent connection pool.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = s.Container.GetAllDevices(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("device query bypassed shared pool: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Container.GetAllDevices(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentCloseClosesBothStoreSurfaces(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := s.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		})
	}
	wg.Wait()
	if err := s.db.PingContext(t.Context()); err == nil {
		t.Error("application pool remained open")
	}
	if _, err := s.Container.GetAllDevices(t.Context()); err == nil {
		t.Error("device pool remained open")
	}
}
