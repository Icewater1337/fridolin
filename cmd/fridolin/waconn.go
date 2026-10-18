package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// The WhatsApp connection: its state as the dashboard shows it, pairing,
// and a new session after WhatsApp logged the device out.

// The connection modes, WAState.Mode, besides those of the pairing.
const (
	modeConnecting   = "connecting"
	modeConnected    = "connected"
	modeDisconnected = "disconnected"
	modePairing      = "pairing"
	modeQRExpired    = "qr-expired"
	modeLoggedOut    = "logged-out"
	modeReplaced     = "replaced"
	// modeOutdated: WhatsApp no longer accepts the bridge's WhatsApp
	// library, and will not until it is updated.
	modeOutdated = "outdated"
	// modeBanned: the account is banned for a while.
	modeBanned = "banned"
	// modeFailed: WhatsApp refused the connection for another reason, and
	// whatsmeow does not try again by itself.
	modeFailed = "failed"
)

// The detail texts the dashboard shows with them.
const (
	detailLoggedOut = "WhatsApp logged this device out. Pair again from the dashboard (bridge.db can stay); remove the old entry under Linked Devices on your phone if it is still there."
	detailOutdated  = "WhatsApp no longer accepts this version of the bridge's WhatsApp library. Update Fridolin (git pull && ./setup.sh), then it connects again."
	detailPairing   = "Scan the code: WhatsApp → Settings → Linked Devices → Link a Device"
)

// After a failure on WhatsApp's side the bridge connects again itself.
// whatsmeow reconnects by itself after a 500 or a 503 without a word, but
// any other code it does not handle reaches the bridge as ConnectFailure
// and ends its attempts: the 5xx among them (501, or one whatsmeow does
// not know) may pass. The wait starts at connectFailureRetry and doubles
// up to connectFailureMaxRetry while the failures go on; a connection
// starts it over.
const (
	connectFailureRetry    = time.Minute
	connectFailureMaxRetry = 30 * time.Minute
)

// failureRetryAfter is the wait before connecting again after a failure on
// WhatsApp's side, when the wait before was prev (0 for none).
func failureRetryAfter(prev time.Duration) time.Duration {
	if prev <= 0 {
		return connectFailureRetry
	}
	return min(2*prev, connectFailureMaxRetry)
}

func (wa *WhatsAppClient) setState(mode, detail string) {
	wa.mu.Lock()
	defer wa.mu.Unlock()
	wa.setStateLocked(mode, detail)
}

func (wa *WhatsAppClient) setStateLocked(mode, detail string) {
	if wa.state.Mode != mode {
		wa.state.Since = time.Now()
	}
	online := wa.onlineLocked()
	select {
	case <-online:
		if mode != modeConnected {
			wa.online = make(chan struct{})
		}
	default:
		if mode == modeConnected {
			close(online)
		}
	}
	wa.state.Mode, wa.state.Detail = mode, detail
	if mode != modePairing {
		wa.state.QRCode = ""
	}
	wa.state.OwnNumber = wa.own.User
}

// setOwn records the bridge account's JIDs, as whatsmeow reported them on
// connecting or pairing; empty ones once it was logged out.
func (wa *WhatsAppClient) setOwn(own, lid types.JID) {
	wa.mu.Lock()
	defer wa.mu.Unlock()
	wa.own, wa.ownLID = own.ToNonAD(), lid.ToNonAD()
	wa.state.OwnNumber = wa.own.User
}

// Connected returns a channel closed while WhatsApp is connected.
func (wa *WhatsAppClient) Connected() <-chan struct{} {
	wa.mu.Lock()
	defer wa.mu.Unlock()
	return wa.onlineLocked()
}

func (wa *WhatsAppClient) onlineLocked() chan struct{} {
	if wa.online == nil {
		wa.online = make(chan struct{})
	}
	return wa.online
}

// State returns the current connection state.
func (wa *WhatsAppClient) State() chat.LinkState {
	wa.mu.Lock()
	defer wa.mu.Unlock()
	return wa.state
}

// Connect connects with the stored session, or starts QR pairing in the
// background when there is none. It does not block on pairing: the code is
// shown on the dashboard (and in the terminal, when there is one).
func (wa *WhatsAppClient) Connect() error {
	cli := wa.cl()
	if cli.Store.ID != nil {
		wa.setState(modeConnecting, "")
		if err := cli.Connect(); err != nil {
			return fmt.Errorf("connecting: %w", err)
		}
		return nil
	}
	return wa.StartPairing()
}

// errPairingLive refuses a second pairing while one runs: both would
// follow the same events.
var errPairingLive = errors.New("pairing is already in progress — scan the code on the dashboard")

// StartPairing begins QR pairing for an unpaired bridge. After WhatsApp
// logged the device out, it first starts a new session (see newSession):
// whatsmeow deleted the device, and its client cannot connect again.
func (wa *WhatsAppClient) StartPairing() error {
	wa.pairMu.Lock()
	defer wa.pairMu.Unlock()
	if wa.pairing {
		return errPairingLive
	}
	cli := wa.cl()
	if cli.Store.Deleted {
		cli = wa.newSession()
	}
	if cli.Store.ID != nil {
		return fmt.Errorf("already paired — this bridge has a stored WhatsApp session")
	}
	if cli.IsConnected() {
		// A connection left from before, the pairing that expired say.
		cli.Disconnect()
	}
	// Ends the code emitter with the pairing (or the client), which
	// otherwise outlives a pairing that is replaced.
	ctx, cancel := context.WithCancel(wa.ctx)
	qrChan, err := cli.GetQRChannel(ctx)
	if err != nil {
		cancel()
		return fmt.Errorf("getting QR channel: %w", err)
	}
	if err := cli.Connect(); err != nil {
		cancel()
		return fmt.Errorf("connecting: %w", err)
	}
	wa.pairing = true
	wa.setState(modePairing, detailPairing)
	go wa.followPairing(ctx, cancel, qrChan)
	return nil
}

// followPairing shows the pairing's codes and ends with it: when its
// channel closes, or its ctx ends.
func (wa *WhatsAppClient) followPairing(ctx context.Context, cancel context.CancelFunc, qrChan <-chan whatsmeow.QRChannelItem) {
	defer func() {
		cancel()
		wa.pairMu.Lock()
		wa.pairing = false
		wa.pairMu.Unlock()
	}()
	tty := platform.IsTerminal(os.Stdout)
	for {
		var item whatsmeow.QRChannelItem
		var ok bool
		select {
		case <-ctx.Done():
			return
		case item, ok = <-qrChan:
			if !ok {
				return
			}
		}
		if item.Event == whatsmeow.QRChannelEventCode {
			wa.mu.Lock()
			wa.state.QRCode, wa.state.QRAt = item.Code, time.Now()
			wa.mu.Unlock()
			if tty {
				fmt.Println("Scan this code with WhatsApp (Settings → Linked Devices → Link a Device),")
				fmt.Println("or open the dashboard:")
				qrterminal.GenerateHalfBlock(item.Code, qrterminal.L, os.Stdout)
			} else {
				log.Printf("WhatsApp pairing: new QR code available on the dashboard")
			}
			continue
		}
		log.Printf("WhatsApp pairing: %s", item.Event)
		if mode, detail, ok := pairingState(item); ok {
			wa.setState(mode, detail)
		}
	}
}

// pairingState is the state a pairing's item other than a code leads to.
// ok is false for one that changes nothing. Every item that ends the
// pairing leads to a state that offers to pair again, or says why not.
func pairingState(item whatsmeow.QRChannelItem) (mode, detail string, ok bool) {
	switch item {
	case whatsmeow.QRChannelSuccess:
		return modeConnected, "Paired", true
	case whatsmeow.QRChannelTimeout:
		return modeQRExpired, "The pairing code expired — start pairing again from the dashboard", true
	case whatsmeow.QRChannelClientOutdated:
		return modeOutdated, detailOutdated, true
	case whatsmeow.QRChannelScannedWithoutMultidevice:
		// The same code can be scanned again.
		return modePairing, "The phone does not have multi-device turned on: turn it on in WhatsApp, then scan the code again", true
	case whatsmeow.QRChannelErrUnexpectedEvent:
		return modeQRExpired, "Pairing stopped unexpectedly — start pairing again from the dashboard", true
	}
	if item.Error != nil {
		return modeQRExpired, "Pairing failed (" + item.Error.Error() + ") — start pairing again from the dashboard", true
	}
	// A passkey step, which the bridge cannot answer.
	return modeQRExpired, "Pairing stopped (" + item.Event + "): the phone asked for a step the bridge does not support — start pairing again from the dashboard", true
}

// newSession replaces the client after WhatsApp logged the device out:
// whatsmeow deleted the device and refuses to connect with it again, so
// pairing again needs a client on a new one. The rest of bridge.db (polls,
// ignored chats, the outbox) stays.
func (wa *WhatsAppClient) newSession() *whatsmeow.Client {
	cli := whatsmeow.NewClient(wa.store.Container.NewDevice(), wa.log)
	cli.AddEventHandler(wa.eventHandler)
	wa.mu.Lock()
	old := wa.client
	wa.client = cli
	wa.own, wa.ownLID = types.EmptyJID, types.EmptyJID
	wa.state.OwnNumber = ""
	wa.mu.Unlock()
	old.RemoveEventHandlers()
	old.Disconnect()
	log.Printf("WhatsApp: started a new session to pair again")
	return cli
}

// intakeDrainWarning is when Disconnect warns about a slow drain.
const intakeDrainWarning = 5 * time.Second

// Disconnect disconnects the client for good: it also ends the attachment
// downloads still running, the group lookups and a pairing. The messages
// that came before are still handed on: the one whose download it cut
// off with its text, without the file. It joins every worker before the
// bridge closes its store, warning after intakeDrainWarning if needed.
func (wa *WhatsAppClient) Disconnect() {
	wa.cancel()
	wa.mu.Lock()
	if wa.retry != nil {
		wa.retry.Stop()
	}
	wa.mu.Unlock()
	// First the connection, so that no more messages come; then the
	// queues hand on what they hold.
	wa.cl().Disconnect()
	wa.intake.close(intakeDrainWarning)
}

// connectionEvent follows the connection's events into the state.
func (wa *WhatsAppClient) connectionEvent(rawEvt any) {
	var retry time.Duration // when the bridge connects again itself
	switch evt := rawEvt.(type) {
	case *events.Connected:
		wa.log.Infof("Connected to WhatsApp")
		// The device holds the account's JIDs from here on.
		st := wa.cl().Store
		wa.setOwn(st.GetJID(), st.GetLID())
		wa.mu.Lock()
		wa.retryWait = 0
		// whatsmeow or a pairing connected first: the attempt waiting
		// would only find the client connected.
		if wa.retry != nil {
			wa.retry.Stop()
		}
		wa.mu.Unlock()
	case *events.PairSuccess:
		wa.log.Infof("Paired with WhatsApp as %s", evt.ID.User)
		wa.setOwn(evt.ID, evt.LID)
	case *events.LoggedOut:
		wa.log.Warnf("Logged out from WhatsApp (reason %v)", evt.Reason)
		wa.setOwn(types.EmptyJID, types.EmptyJID)
	case *events.TemporaryBan:
		wa.log.Warnf("WhatsApp: %s", evt)
		if evt.Expire > 0 {
			wa.connectAfter(evt.Expire)
		}
	case *events.ConnectFailure:
		wa.log.Warnf("WhatsApp refused the connection: %s %s", evt.Reason, evt.Message)
		if evt.Reason >= 500 {
			wa.mu.Lock()
			wa.retryWait = failureRetryAfter(wa.retryWait)
			retry = wa.retryWait
			wa.mu.Unlock()
			wa.connectAfter(retry)
		}
	case *events.KeepAliveTimeout:
		wa.log.Warnf("WhatsApp keepalive timed out (%d in a row)", evt.ErrorCount)
	}
	if mode, detail, ok := connectionState(rawEvt, time.Now(), retry); ok {
		wa.setState(mode, detail)
	}
}

// connectionState is the state a connection event leads to, at now, when
// the bridge connects again after retry (0 when it does not); ok is false
// for an event that changes nothing.
func connectionState(rawEvt any, now time.Time, retry time.Duration) (mode, detail string, ok bool) {
	switch evt := rawEvt.(type) {
	case *events.Connected:
		return modeConnected, "", true
	case *events.PairSuccess:
		return modeConnected, "Paired", true
	case *events.Disconnected:
		return modeDisconnected, "Connection lost — reconnecting automatically", true
	case *events.KeepAliveTimeout:
		// The connection may come back by itself (KeepAliveRestored), or be
		// found dead and replaced; meanwhile nothing goes through.
		return modeConnecting, fmt.Sprintf("WhatsApp has not answered since %s — waiting for the connection", evt.LastSuccess.Local().Format("15:04")), true
	case *events.KeepAliveRestored:
		return modeConnected, "", true
	case *events.StreamReplaced:
		return modeReplaced, "Another program is using this WhatsApp session — is a second bridge running?", true
	case *events.LoggedOut:
		return modeLoggedOut, detailLoggedOut, true
	case *events.ClientOutdated:
		return modeOutdated, detailOutdated, true
	case *events.TemporaryBan:
		d := fmt.Sprintf("WhatsApp banned this account for a while (%s).", evt.Code)
		if evt.Expire > 0 {
			d += fmt.Sprintf(" The ban ends around %s; the bridge connects again then.", now.Add(evt.Expire).Local().Format("Jan 2 15:04"))
		}
		return modeBanned, d, true
	case *events.ConnectFailure:
		d := fmt.Sprintf("WhatsApp refused the connection (%s", evt.Reason)
		if evt.Message != "" {
			d += ": " + evt.Message
		}
		d += ")."
		if retry > 0 {
			d += fmt.Sprintf(" The bridge tries again in %v.", retry)
		} else {
			d += " Restart the bridge to try again."
		}
		return modeFailed, d, true
	case events.PermanentDisconnect:
		return modeFailed, "WhatsApp ended the connection (" + evt.PermanentDisconnectDescription() + "). Restart the bridge to try again.", true
	}
	return "", "", false
}

// connectAfter connects again once d has passed, unless the client was
// disconnected for good by then: whatsmeow does not, after a ban ends or
// after a failure on WhatsApp's side. One such attempt waits at a time,
// the one the latest event asked for.
func (wa *WhatsAppClient) connectAfter(d time.Duration) {
	wa.mu.Lock()
	defer wa.mu.Unlock()
	if wa.retry != nil {
		wa.retry.Stop()
	}
	wa.retry = time.AfterFunc(d, func() {
		if wa.ctx.Err() != nil {
			return
		}
		if err := wa.Connect(); err != nil {
			log.Printf("WhatsApp: connecting again: %v", err)
		}
	})
}
