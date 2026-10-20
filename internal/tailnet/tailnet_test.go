package tailnet

import (
	"context"
	"errors"
	"testing"
)

func TestFake(t *testing.T) {
	f := &Fake{Status: Status{DNSName: "fridolin-core.example.ts.net"}, Who: Identity{LoginName: "you@example.com", Node: "laptop"}}
	st, err := f.Up(context.Background())
	if err != nil || st.ShortName() != "fridolin-core" {
		t.Fatalf("Up = %+v, %v", st, err)
	}
	if _, err := f.ListenTLS("tcp", ":443"); err == nil {
		t.Error("ListenTLS without HTTPS in the tailnet worked")
	}
	ln, err := f.Listen("tcp", ":80")
	if err != nil {
		t.Fatal(err)
	}
	if f.Addr(":80") != ln.Addr().String() {
		t.Errorf("Addr(:80) = %q, listener on %s", f.Addr(":80"), ln.Addr())
	}
	if id, _ := f.WhoIs(context.Background(), "100.64.0.2:1234"); id.String() != "you@example.com on laptop" {
		t.Errorf("WhoIs = %s", id)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Listen("tcp", ":81"); !errors.Is(err, ErrClosed) {
		t.Errorf("Listen after Close: %v", err)
	}
	if p, err := Port(":8766"); err != nil || p != "8766" {
		t.Errorf("Port = %q, %v", p, err)
	}
	if _, err := Port("8766"); err == nil {
		t.Error("Port of an address without a colon worked")
	}
	if _, err := New(Options{}); err == nil {
		t.Error("New without a hostname and a folder worked")
	}
	// New reaches no network before Up or Listen.
	n, err := New(Options{Hostname: "fridolin-core", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if (Identity{}).String() != "unknown user" || n == nil {
		t.Error("Identity.String of nobody")
	}
}
