package app

import (
	"log"
	"time"
)

// StopOptions supplies the application's dependency layers in shutdown order.
// Closing intake precedes the HTTP listener and background producers. Pending
// work is persisted while storage is open, and brains finish before transport
// and storage close. Callbacks are required, except Background may be empty.
type StopOptions struct {
	CloseIntake     func()
	HTTPGrace       time.Duration
	Background      []func()
	KeepPending     func()
	Brains          func()
	Outbox          func()
	OTP             func()
	Disconnect      func()
	ConnectionGrace time.Duration
	EndIntake       func()
	CloseStore      func() error
	ReleaseLock     func()
}

// Stop tears down each layer once. Concurrent callers wait for the first stop
// to finish, so none can release the instance lock while storage is still open.
func (s *Supervisor) Stop(o StopOptions) {
	s.stopOnce.Do(func() {
		o.CloseIntake()
		s.StopHTTP(o.HTTPGrace)
		for _, stop := range o.Background {
			stop()
		}
		o.KeepPending()
		o.Brains()
		o.Outbox()
		o.OTP()
		o.Disconnect()
		if !s.WaitConnections(o.ConnectionGrace) {
			log.Printf("WhatsApp: still connecting %v after the disconnect; stopping without waiting for it", o.ConnectionGrace)
		}
		o.EndIntake()
		if err := o.CloseStore(); err != nil {
			log.Printf("closing store: %v", err)
		}
		o.ReleaseLock()
		log.Println("Bridge stopped.")
	})
}
