package app

import (
	"log"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/otp"
)

// handOffCode hands a code the OTP guard caught in m to the local script
// that asked for it: it is written to <data_dir>/otp/<brain>.json,
// readable only by this user, until it expires (otpHandoffTTL). Brains
// cannot read it (the sandbox denies the data folder), and it never
// reaches the history or the model. A newer code for the brain replaces
// it.
func (b *App) handOffCode(br *brain.Brain, m brain.Inbound, c brain.OTPCatch) {
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

// takeCode hands the code that answers request to the script that asked
// for it through POST /otp/take, once: its hand-off file goes with it.
func (b *App) takeCode(request string) (httpapi.OTPCodeResponse, bool, error) {
	h, ok, err := b.otp.Take(b.cfgs.Boot().OTPDir(), request, time.Now())
	if err != nil || !ok {
		return httpapi.OTPCodeResponse{}, ok, err
	}
	log.Printf("otp: a script took the code for request %s", request)
	return httpapi.OTPCodeResponse{
		Brain: h.Brain, Chat: h.Chat, Sender: h.Sender, Code: h.Code, Typed: h.Typed,
		Received: h.Received, ReceivedUnixMS: h.ReceivedUnixMS,
		Request: h.Request, RequestedUnixMS: h.RequestedUnixMS, ExpiresUnixMS: h.ExpiresUnixMS,
	}, true, nil
}
