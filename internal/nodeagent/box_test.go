package nodeagent

import (
	"bufio"
	"context"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

func TestBoxJoinsAndRunsOverItsDialer(t *testing.T) {
	c := startCore(t, t.TempDir())
	// The core's tailnet name, which only the box's dialer resolves.
	const tailnetCore = "fridolin-core.example.ts.net:8766"
	var dials atomic.Int32
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != tailnetCore {
			t.Errorf("the box dialed %s, want the core's tailnet address", addr)
		}
		dials.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, network, c.addr)
	}
	dir := t.TempDir()
	box := &BoxConfig{Hostname: BoxHostname("Ice Base"), Fride: ""}
	j, err := Join(context.Background(), JoinOptions{Dir: dir, Core: tailnetCore, Code: c.code(t), Name: "icebase",
		Capabilities: nodeproto.Capabilities{Host: true}, Box: box, Dial: dial, Version: "test", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Box == nil || cfg.Box.Hostname != "fridolin-box-ice-base" {
		t.Fatalf("node.json's box section: %+v", cfg.Box)
	}
	a, err := Load(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	a.Dial, a.Logf, a.MinBackoff, a.MaxBackoff, a.HeartbeatEvery = dial, t.Logf, 10*time.Millisecond, 100*time.Millisecond, 50*time.Millisecond
	runAgent(t, a)
	waitFor(t, "the box to connect", func() bool { return c.svc.Online(j.ID) })
	if dials.Load() < 2 {
		t.Fatalf("the box dialed %d times, want the join and the run through its dialer", dials.Load())
	}
	if d := a.declared(); d.Update != nil {
		t.Fatalf("a box offers update: %+v", d.Update)
	}
}

func TestBoxConfigCheck(t *testing.T) {
	for _, tc := range []struct {
		box *BoxConfig
		ok  bool
	}{
		{nil, true},
		{&BoxConfig{Hostname: "fridolin-box-icebase", Fride: BoxFride}, true},
		{&BoxConfig{Hostname: "fridolin-box-icebase"}, true},
		{&BoxConfig{Hostname: ""}, false},
		{&BoxConfig{Hostname: "Upper"}, false},
		{&BoxConfig{Hostname: "-x"}, false},
		{&BoxConfig{Hostname: "ok", Fride: "fride"}, false},
		{&BoxConfig{Hostname: "ok", Fride: "fride:0"}, false},
	} {
		if err := tc.box.Check(); (err == nil) != tc.ok {
			t.Errorf("%+v: Check() = %v, want ok %v", tc.box, err, tc.ok)
		}
	}
	if got := BoxHostname("Ann's PC.local"); got != "fridolin-box-ann-s-pc-local" {
		t.Errorf("BoxHostname = %q", got)
	}
	if got := BoxHostname(strings.Repeat("x", 80)); len(got) > 63 || CheckTailnetHostname(got) != nil {
		t.Errorf("a long name gives %q", got)
	}
}

func TestForwardCopiesBothWaysAndRefusesOthers(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, allowed := range []bool{true, false} {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go forward(ctx, ln, func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", echo.Addr().String())
		}, func(net.Addr) bool { return allowed }, t.Logf)
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.WriteString(c, "ping\n")
		line, err := bufio.NewReader(c).ReadString('\n')
		c.Close()
		if allowed && line != "ping\n" {
			t.Errorf("through the forwarder: %q, %v", line, err)
		}
		if !allowed && err == nil {
			t.Errorf("a refused connection got an answer: %q", line)
		}
	}
}
