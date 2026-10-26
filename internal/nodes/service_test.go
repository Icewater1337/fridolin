package nodes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// serveNodes serves a node port on loopback; setup runs before it
// opens.
func serveNodes(t *testing.T, setup func(*Service)) *Service {
	t.Helper()
	svc, err := New(Options{Dir: t.TempDir(), Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	setup(svc)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Serve(ln); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	return svc
}

// dialAsNode goes through hello, challenge and auth as the node of key
// does, with join code, and returns the connection.
func dialAsNode(t *testing.T, addr string, key ed25519.PrivateKey, code string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws://"+addr+nodeproto.Path, &websocket.DialOptions{Subprotocols: []string{nodeproto.Subprotocol}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	send := func(typ string, body any) {
		data, err := nodeproto.Encode(typ, "", body)
		if err == nil {
			err = ws.Write(ctx, websocket.MessageText, data)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	pub := key.Public().(ed25519.PublicKey)
	id := nodeproto.DeviceIDFor(pub)
	send(nodeproto.TypeHello, nodeproto.Hello{DeviceID: id, PublicKey: pub, Name: "laptop", OS: "linux", Arch: "amd64", Version: "v1", Capabilities: nodeproto.Capabilities{Host: true}, JoinCode: code})
	env, err := readEnvelope(ctx, ws)
	if err != nil || env.Type != nodeproto.TypeChallenge {
		t.Fatalf("the answer to hello: %+v %v", env, err)
	}
	ch, err := nodeproto.DecodeBody[nodeproto.Challenge](env)
	if err != nil {
		t.Fatal(err)
	}
	send(nodeproto.TypeAuth, nodeproto.Auth{Signature: nodeproto.Sign(key, id, ch.Nonce)})
	return ws
}

// An approval or a revocation while a node connects reaches it, wherever
// the connection is then (#324): the welcome comes first, as the agent
// wants it, and the node hears the change in it or right after it.
// Before, an approval after the handshake read the node and before the
// connection was live was told nobody, and the node stayed "commands
// pending" until it connected again; one just after it was live reached
// the node before its welcome, which broke its handshake.
func TestNodeHearsWhatChangedWhileItConnected(t *testing.T) {
	for _, stage := range []string{"handshook", "registered"} {
		for _, change := range []string{"approve", "revoke"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				var svc *Service
				act := func(id string) {
					if change == "revoke" {
						if _, err := svc.Revoke(id); err != nil {
							t.Error(err)
						}
						return
					}
					if _, err := svc.ApproveDevice(id); err != nil {
						t.Error(err)
					}
					if _, err := svc.ApproveCommands(id); err != nil {
						t.Error(err)
					}
				}
				svc = serveNodes(t, func(s *Service) {
					if stage == "handshook" {
						s.handshook = act
					} else {
						s.registered = act
					}
				})
				code, _, err := svc.NewJoinCode()
				if err != nil {
					t.Fatal(err)
				}
				_, key, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				ws := dialAsNode(t, svc.Addr(), key, code)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				for i := 0; ; i++ {
					env, err := readEnvelope(ctx, ws)
					if err != nil {
						t.Fatalf("the node never heard of the change (%s): %v", change, err)
					}
					// A close may come at any time; anything else after
					// the welcome.
					if i == 0 && env.Type != nodeproto.TypeWelcome && env.Type != nodeproto.TypeClose {
						t.Fatalf("the first message after auth is a %s, want the welcome", env.Type)
					}
					if change == "revoke" {
						if env.Type == nodeproto.TypeClose {
							if c, err := nodeproto.DecodeBody[nodeproto.Close](env); err != nil || c.Reason != nodeproto.CloseRevoked {
								t.Fatalf("a node revoked while it connected was closed with %+v %v", c, err)
							}
							return
						}
						continue
					}
					if env.Type != nodeproto.TypeWelcome && env.Type != nodeproto.TypeState {
						t.Fatalf("the node was sent a %s", env.Type)
					}
					st, err := nodeproto.DecodeBody[nodeproto.State](env)
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("%s: device %s, commands %s, approved %s", env.Type, st.Device, st.Commands, st.Approved.Summary())
					if st.Device == nodeproto.DeviceApproved && st.Commands == nodeproto.CommandsApproved && st.Approved.Host {
						break
					}
				}
				waitOnline(t, svc, nodeproto.DeviceIDFor(key.Public().(ed25519.PublicKey)))
			})
		}
	}
}

// A node whose state did not change while it connected hears its welcome
// and nothing more.
func TestNodeConnectingWithoutChangeHearsOnlyTheWelcome(t *testing.T) {
	svc := serveNodes(t, func(*Service) {})
	code, _, err := svc.NewJoinCode()
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ws := dialAsNode(t, svc.Addr(), key, code)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if env, err := readEnvelope(ctx, ws); err != nil || env.Type != nodeproto.TypeWelcome {
		t.Fatalf("the first message after auth: %+v %v", env, err)
	}
	waitOnline(t, svc, nodeproto.DeviceIDFor(key.Public().(ed25519.PublicKey)))
	quiet, cancelQuiet := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelQuiet()
	if env, err := readEnvelope(quiet, ws); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("after the welcome the node was sent %+v %v", env, err)
	}
}

// A call that an approval allows reaches the node after the state that
// says so, also when it goes out before the approval's own push: the node
// checks a call against what it was told, and refused it before.
func TestNodeHearsTheStateBeforeACallItAllows(t *testing.T) {
	var (
		svc   *Service
		armed atomic.Bool
		msgs  = make(chan nodeproto.Envelope, 16)
		first = make(chan nodeproto.Envelope, 1)
		calls = make(chan error, 1)
	)
	svc = serveNodes(t, func(s *Service) {
		s.pushing = func(id string) {
			if !armed.CompareAndSwap(true, false) {
				return
			}
			// The approval is recorded and not yet told: a call now.
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				calls <- svc.Call(ctx, id, nodeproto.MethodHostInfo, struct{}{}, nil)
			}()
			select {
			case env := <-msgs:
				first <- env
			case <-time.After(5 * time.Second):
				t.Error("the call sent the node nothing")
				close(first)
			}
		}
	})
	code, _, err := svc.NewJoinCode()
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := nodeproto.DeviceIDFor(key.Public().(ed25519.PublicKey))
	ws := dialAsNode(t, svc.Addr(), key, code)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		defer close(msgs)
		for {
			env, err := readEnvelope(ctx, ws)
			if err != nil {
				return
			}
			msgs <- env
		}
	}()
	next := func(what string) nodeproto.Envelope {
		t.Helper()
		select {
		case env, ok := <-msgs:
			if !ok {
				t.Fatalf("the connection ended before %s", what)
			}
			return env
		case <-ctx.Done():
			t.Fatalf("no %s", what)
		}
		return nodeproto.Envelope{}
	}
	if env := next("the welcome"); env.Type != nodeproto.TypeWelcome {
		t.Fatalf("the first message after auth is a %s", env.Type)
	}
	waitOnline(t, svc, id)
	if _, err := svc.ApproveDevice(id); err != nil {
		t.Fatal(err)
	}
	if env := next("the device's approval"); env.Type != nodeproto.TypeState {
		t.Fatalf("approving the device sent a %s", env.Type)
	}

	armed.Store(true)
	if _, err := svc.ApproveCommands(id); err != nil {
		t.Fatal(err)
	}
	env, ok := <-first
	if !ok {
		t.FailNow()
	}
	if env.Type != nodeproto.TypeState {
		t.Fatalf("the call's %s reached the node before the state that allows it", env.Type)
	}
	if st, err := nodeproto.DecodeBody[nodeproto.State](env); err != nil || st.Commands != nodeproto.CommandsApproved || !st.Approved.Host {
		t.Fatalf("the state before the call: %+v %v", st, err)
	}
	// The call follows; its answer ends it.
	for {
		env := next("the call")
		if env.Type == nodeproto.TypeState {
			continue
		}
		if env.Type != nodeproto.TypeRequest {
			t.Fatalf("the node was sent a %s", env.Type)
		}
		data, err := nodeproto.Encode(nodeproto.TypeResponse, env.ID, nodeproto.Response{Result: json.RawMessage(`{}`)})
		if err == nil {
			err = ws.Write(ctx, websocket.MessageText, data)
		}
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if err := <-calls; err != nil {
		t.Fatalf("the call: %v", err)
	}
}

func waitOnline(t *testing.T, svc *Service, id string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !svc.Online(id); {
		if time.Now().After(deadline) {
			t.Fatal("the node is not online")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
