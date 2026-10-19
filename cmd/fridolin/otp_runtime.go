package main

import (
	"log"
	"time"

	"github.com/Icewater1337/fridolin/internal/otp"
)

// handOffCode hands a code the OTP guard caught in m to the local script
// that asked for it: it is written to <data_dir>/otp/<brain>.json,
// readable only by this user, until it expires (otpHandoffTTL). Brains
// cannot read it (the sandbox denies the data folder), and it never
// reaches the history or the model. A newer code for the brain replaces
// it.
func (b *Bridge) handOffCode(br *Brain, m Inbound, c OTPCatch) {
	now := time.Now()
	h := otp.Record{
		Brain: br.ID(), Chat: m.JID, Sender: m.Sender, Code: c.Code, Typed: c.Typed,
		Received: now.Format(time.RFC3339), ReceivedUnixMS: now.UnixMilli(),
		Request: c.Request.ID, RequestedUnixMS: c.Request.At.UnixMilli(),
		ExpiresUnixMS: now.Add(otp.HandoffTTL).UnixMilli(),
	}
	if err := b.otp.Write(b.cfgs.Boot().OTPDir(), h); err != nil {
		log.Printf("brain %s: handing over a one-time code: %v", br.ID(), err)
	}
}
