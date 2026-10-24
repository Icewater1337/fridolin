package app_test

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/app"
)

func TestSupervisorStopKeepsLayerOrderAndRunsOnce(t *testing.T) {
	t.Parallel()
	var supervisor app.Supervisor
	var order []string
	record := func(name string) func() { return func() { order = append(order, name) } }
	options := app.StopOptions{
		CloseIntake: record("intake"), HTTPGrace: time.Second,
		Background:  []func(){record("backup"), record("plaud"), record("site")},
		KeepPending: record("pending"), Brains: record("brains"),
		Outbox: record("outbox"), OTP: record("otp"), Disconnect: record("disconnect"),
		ConnectionGrace: time.Second, EndIntake: record("end intake"),
		CloseStore:  func() error { record("store")(); return errors.New("close failed") },
		ReleaseLock: record("lock"),
	}
	var callers sync.WaitGroup
	for range 8 {
		callers.Go(func() { supervisor.Stop(options) })
	}
	callers.Wait()
	want := []string{"intake", "backup", "plaud", "site", "pending", "brains", "outbox", "otp", "disconnect", "end intake", "store", "lock"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("stop order: %v, want %v", order, want)
	}
}
