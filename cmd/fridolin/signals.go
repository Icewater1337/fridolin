package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
)

// stopOnSignals keeps the second-signal force-stop behavior during teardown.
// Its cleanup unregisters signals and joins the watcher when serve returns.
func stopOnSignals(cancel context.CancelCauseFunc, killRuns func()) (signalled func() bool, stop func()) {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	signalled, stopWatch := watchSignals(signals, cancel, func() { killRuns(); os.Exit(1) })
	return signalled, func() { signal.Stop(signals); stopWatch() }
}

func watchSignals(signals <-chan os.Signal, cancel context.CancelCauseFunc, force func()) (func() bool, func()) {
	var got atomic.Bool
	done, exited := make(chan struct{}), make(chan struct{})
	var once sync.Once
	go func() {
		defer close(exited)
		select {
		case <-done:
			return
		case sig := <-signals:
			got.Store(true)
			log.Printf("Received signal %s, shutting down… (send it again to stop at once)", sig)
			cancel(fmt.Errorf("received signal %s", sig))
		}
		select {
		case <-done:
			return
		case sig := <-signals:
			log.Printf("Received signal %s again: stopping the runs still going and exiting", sig)
			force()
		}
	}()
	return got.Load, func() { once.Do(func() { close(done) }); <-exited }
}
