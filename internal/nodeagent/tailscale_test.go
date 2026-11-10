package nodeagent

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

// fakeTailscale is a machine's Tailscale: state until Up switches it on.
type fakeTailscale struct {
	mu    sync.Mutex
	state string
	ups   int
	upErr error
}

func (f *fakeTailscale) State(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, nil
}

func (f *fakeTailscale) Up(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ups++
	if f.upErr != nil {
		return f.upErr
	}
	f.state = "Running"
	return nil
}

func (f *fakeTailscale) count() (string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.ups
}

// TestSwitchesTailscaleOn: the core cannot even be looked up while the
// machine's Tailscale is off. Once the core has been out of reach for the
// grace, the agent switches Tailscale on, connects, and its hello tells
// the core, which hears it once.
func TestSwitchesTailscaleOn(t *testing.T) {
	heard := make(chan nodeproto.TailscaleOn, 4)
	svc, err := nodes.New(nodes.Options{Dir: t.TempDir(), Logf: t.Logf, OnTailscaleOn: func(_ *nodes.Node, on nodeproto.TailscaleOn) { heard <- on }})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Serve(ln); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	c := &core{svc: svc, addr: svc.Addr()}
	a := joinNode(t, c, DefaultCapabilities(runtime.GOOS))
	if _, err := svc.ApproveDevice(a.ID()); err != nil {
		t.Fatal(err)
	}

	ts := &fakeTailscale{state: tailscaleStopped}
	a.Tailscale, a.TailscaleGrace = ts, 100*time.Millisecond
	a.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if st, _ := ts.count(); st != "Running" {
			return nil, &net.DNSError{Err: "no such host", Name: "fridolin-core.example.ts.net", IsNotFound: true}
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	start := time.Now()
	runAgent(t, a)
	waitFor(t, "the agent to connect", a.Connected)
	if _, ups := ts.count(); ups != 1 {
		t.Fatalf("tailscale up ran %d times, want 1", ups)
	}
	select {
	case on := <-heard:
		if on.Down.Before(start) || on.On.Sub(on.Down) < a.TailscaleGrace {
			t.Errorf("the core heard %+v: down before the agent started (%s), or on before the grace", on, start)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the core did not hear that the node switched Tailscale on")
	}
	select {
	case on := <-heard:
		t.Errorf("the core heard it twice: %+v", on)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestTailscaleHealWaits: the agent switches on only a Tailscale that is
// off and signed in, once the core has been out of reach for the grace;
// after a failed try it waits, longer each time; and a core that answers
// ends the outage.
func TestTailscaleHealWaits(t *testing.T) {
	ctx := context.Background()
	unreachable := &net.DNSError{Err: "no such host", Name: "fridolin-core.example.ts.net", IsNotFound: true}
	for _, state := range []string{"Running", "NeedsLogin"} {
		ts := &fakeTailscale{state: state}
		a := &Agent{Logf: t.Logf, Tailscale: ts, TailscaleGrace: time.Minute}
		a.ts.down = time.Now().Add(-time.Hour)
		a.tailscaleHeal(ctx, unreachable)
		if _, ups := ts.count(); ups != 0 {
			t.Errorf("%s: tailscale up ran %d times", state, ups)
		}
	}

	ts := &fakeTailscale{state: tailscaleStopped, upErr: errors.New("access denied")}
	a := &Agent{Logf: t.Logf, Tailscale: ts, TailscaleGrace: time.Minute}
	a.tailscaleHeal(ctx, unreachable)
	if _, ups := ts.count(); ups != 0 || a.ts.down.IsZero() {
		t.Fatalf("within the grace: %d ups, down %v", ups, a.ts.down)
	}
	a.ts.down = time.Now().Add(-2 * time.Minute)
	a.tailscaleHeal(ctx, unreachable)
	first := a.ts.next
	a.tailscaleHeal(ctx, unreachable)
	if _, ups := ts.count(); ups != 1 || time.Until(first) < time.Minute {
		t.Fatalf("after the grace: %d ups, next try in %v", ups, time.Until(first))
	}
	a.ts.next = time.Now().Add(-time.Second)
	a.tailscaleHeal(ctx, unreachable)
	if _, ups := ts.count(); ups != 2 || time.Until(a.ts.next) < 3*time.Minute || a.ts.report != nil {
		t.Fatalf("the second try: %d ups, next in %v, report %+v", ups, time.Until(a.ts.next), a.ts.report)
	}
	a.tailscaleHeal(ctx, &CloseError{Reason: nodeproto.CloseShutdown})
	if !a.ts.down.IsZero() || a.ts.tries != 0 {
		t.Errorf("a core that answered left the outage open: %+v", a.ts)
	}

	ts.upErr = nil
	a.ts.down = time.Now().Add(-2 * time.Minute)
	a.tailscaleHeal(ctx, unreachable)
	if st, ups := ts.count(); st != "Running" || ups != 3 || a.ts.report == nil || a.ts.report.Down != a.ts.down {
		t.Fatalf("switched on: %s after %d ups, report %+v", st, ups, a.ts.report)
	}
	a.tailscaleReached(true)
	if a.ts.report != nil || !a.ts.down.IsZero() {
		t.Errorf("a welcome left %+v", a.ts)
	}
}

// TestTailscaleCLIAnswersAsCLI: the macOS app's binary, started by a
// launchd agent, opens the app instead of answering unless
// TAILSCALE_BE_CLI=1 is set, as it was on the Mac mini (2026-11-10). The
// fake answers like it: the CLI's answer only with the variable.
func TestTailscaleCLIAnswersAsCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for tailscale")
	}
	dir := t.TempDir()
	bin, upped := filepath.Join(dir, "Tailscale"), filepath.Join(dir, "upped")
	writeScript(t, bin, `if [ "$TAILSCALE_BE_CLI" != 1 ]; then echo "The Tailscale GUI failed to start" >&2; exit 1; fi
case "$1" in
status) echo '{"BackendState":"Stopped"}'; exit 1 ;;
up) : > "`+upped+`" ;;
esac`)
	ctx := context.Background()

	if st, err := TailscaleCLI(bin, []string{"PATH=/usr/bin:/bin"}).State(ctx); err == nil {
		t.Fatalf("without TAILSCALE_BE_CLI the fake answered %q", st)
	}
	ts := TailscaleCLI(bin, TailscaleCLIEnv([]string{"PATH=/usr/bin:/bin"}))
	st, err := ts.State(ctx)
	if err != nil || st != tailscaleStopped {
		t.Fatalf("State: %q, %v; want %s", st, err, tailscaleStopped)
	}
	if err := ts.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(upped); err != nil {
		t.Errorf("up did not reach the CLI: %v", err)
	}
}
