package app

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"

	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/whatsapp"

	"github.com/Icewater1337/fridolin/internal/chat"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Command integration uses only normal construction and the production
// processor/event callbacks; transport internals stay in package tests.
type testWhatsApp struct {
	*whatsapp.Client
	handler InboundHandler
	inbound chan brain.Inbound
	notices chan string
	mu      sync.Mutex
	logs    []string
}
type testWaLogger struct{ tw *testWhatsApp }

func (l testWaLogger) add(level, msg string, args ...interface{}) {
	l.tw.mu.Lock()
	defer l.tw.mu.Unlock()
	l.tw.logs = append(l.tw.logs, level+" "+fmt.Sprintf(msg, args...))
}
func (l testWaLogger) Debugf(msg string, args ...interface{}) { l.add("DEBUG", msg, args...) }
func (l testWaLogger) Infof(msg string, args ...interface{})  { l.add("INFO", msg, args...) }
func (l testWaLogger) Warnf(msg string, args ...interface{})  { l.add("WARN", msg, args...) }
func (l testWaLogger) Errorf(msg string, args ...interface{}) { l.add("ERROR", msg, args...) }
func (l testWaLogger) Sub(string) waLog.Logger                { return l }
func newTestWhatsApp(t *testing.T, configure ...func(*whatsapp.Options)) *testWhatsApp {
	t.Helper()
	cfg, err := config.Resolve(&config.File{AllowedNumbers: []string{"15551234567"}, MediaDir: t.TempDir(), MaxChunkSize: 4000}, config.Paths{Home: t.TempDir()}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	tw := &testWhatsApp{inbound: make(chan brain.Inbound, 4), notices: make(chan string, 4)}
	tw.handler = func(_ context.Context, m brain.Inbound) { tw.inbound <- m }
	opts := whatsapp.Options{Settings: whatsAppSettings(func() config.Settings { return *cfg }), Container: fixedDeviceContainer{&store.Device{}}, Chunks: outboundmsg.Chunk, Identity: whatsAppIdentity(), Outbound: whatsAppOutbound(), Text: whatsAppText(), Inbox: mediaInboxAt, Log: testWaLogger{tw},
		OnInbound: func(ctx context.Context, m chat.Inbound) { tw.handler(ctx, brain.Inbound{Inbound: m}) }, Notify: func(jid, text string) { tw.notices <- jid + ": " + text }}
	for _, apply := range configure {
		apply(&opts)
	}
	wa, err := whatsapp.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	tw.Client = wa
	t.Cleanup(wa.Disconnect)
	return tw
}
func (tw *testWhatsApp) feed(events ...*events.Message) {
	for _, ev := range events {
		tw.HandleEvent(ev)
	}
}
