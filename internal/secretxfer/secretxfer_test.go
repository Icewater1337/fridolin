package secretxfer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
)

// fakeNet is a tailnet on loopback: who answers WhoIs for every address.
type fakeNet struct {
	self Peer
	who  func(remote string) (Peer, error)
	// dial, when set, replaces the dial (a party in the middle).
	dial func(ctx context.Context, addr string) (net.Conn, error)
}

func (f *fakeNet) Self(context.Context) (Peer, error) { return f.self, nil }
func (f *fakeNet) Listen(ctx context.Context) (net.Listener, string, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	return ln, ln.Addr().String(), nil
}
func (f *fakeNet) Dial(ctx context.Context, addr string) (net.Conn, error) {
	if f.dial != nil {
		return f.dial(ctx, addr)
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
}
func (f *fakeNet) WhoIs(_ context.Context, remote string) (Peer, error) { return f.who(remote) }

var (
	owner   = Peer{Login: "you@example.com", Node: "mini.example.ts.net"}
	winbox  = Peer{Login: "you@example.com", Node: "winbox.example.ts.net"}
	strange = Peer{Login: "someone@example.com", Node: "theirs.example.ts.net"}
)

func sameNet(self, other Peer) *fakeNet {
	return &fakeNet{self: self, who: func(string) (Peer, error) { return other, nil }}
}

// memStore is a host secret store in memory; failOn makes Set fail.
type memStore struct {
	mu     sync.Mutex
	m      map[string]string
	failOn string
}

func (s *memStore) Get(n string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[n]
	if !ok {
		return "", fmt.Errorf("%s: %w", n, hostsecrets.ErrNotFound)
	}
	return v, nil
}
func (s *memStore) Set(n, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n == s.failOn {
		return errors.New("access denied")
	}
	s.m[n] = v
	return nil
}
func (s *memStore) Has(n string) (bool, error) { _, err := s.Get(n); return err == nil, nil }
func (s *memStore) Delete(n string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, n)
	return nil
}

var allowed = []string{hostsecrets.AgeIdentity, hostsecrets.B2VaultKeyID, hostsecrets.B2VaultKey, hostsecrets.ClaudeOAuthToken}

var sent = map[string]string{
	hostsecrets.AgeIdentity:  "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ",
	hostsecrets.B2VaultKeyID: "0012345abcdef0000000001",
	hostsecrets.B2VaultKey:   "K001secret+/=",
}

type run struct {
	recv      Result
	recvErr   error
	send      Result
	sendErr   error
	recvWords string
	sendWords string
}

// transfer runs a receiver and a sender against each other. code edits
// the code the sender uses; confirm answers the words.
func transfer(t *testing.T, rnet, snet *fakeNet, store *memStore, code func(string) string, confirm bool, replace bool) run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var r run
	ready := make(chan [2]string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.recv, r.recvErr = Receive(ctx, ReceiveOptions{
			Net: rnet, Store: store, Allowed: allowed, Replace: replace, Timeout: 10 * time.Second,
			Ready:     func(addr, c string) { ready <- [2]string{addr, c} },
			Connected: func(_ Peer, w string) { r.recvWords = w },
		})
	}()
	var rc [2]string
	select {
	case rc = <-ready:
	case <-done:
		t.Fatalf("the receiver ended before it listened: %v", r.recvErr)
	}
	r.send, r.sendErr = Send(ctx, SendOptions{
		Net: snet, To: rc[0], Code: code(rc[1]), Secrets: sent, From: "mini", Timeout: 10 * time.Second,
		Confirm: func(_ Peer, w string) (bool, error) { r.sendWords = w; return confirm, nil },
	})
	<-done
	return r
}

func keep(c string) string { return c }

func TestTransfer(t *testing.T) {
	store := &memStore{m: map[string]string{hostsecrets.B2VaultKeyID: sent[hostsecrets.B2VaultKeyID]}}
	r := transfer(t, sameNet(winbox, owner), sameNet(owner, winbox), store, func(c string) string { return strings.ToUpper(c) }, true, false)
	if r.recvErr != nil || r.sendErr != nil {
		t.Fatalf("receive: %v; send: %v", r.recvErr, r.sendErr)
	}
	if r.recvWords == "" || r.recvWords != r.sendWords || len(strings.Fields(r.recvWords)) != 4 {
		t.Errorf("words %q and %q", r.recvWords, r.sendWords)
	}
	for n, v := range sent {
		if store.m[n] != v {
			t.Errorf("%s = %q", n, store.m[n])
		}
	}
	if want := []string{hostsecrets.AgeIdentity, hostsecrets.B2VaultKey}; !slices.Equal(r.recv.Stored, want) || !slices.Equal(r.send.Stored, want) {
		t.Errorf("stored %q / %q", r.recv.Stored, r.send.Stored)
	}
	if !slices.Equal(r.send.Unchanged, []string{hostsecrets.B2VaultKeyID}) || r.recv.From != "mini" || r.recv.Peer.Node != owner.Node {
		t.Errorf("%+v %+v", r.send, r.recv)
	}
}

func TestTransferWrongCode(t *testing.T) {
	store := &memStore{m: map[string]string{}}
	r := transfer(t, sameNet(winbox, owner), sameNet(owner, winbox), store, func(string) string { return "zzzz-zzzz" }, true, false)
	if !errors.Is(r.sendErr, ErrWrongCode) || !errors.Is(r.recvErr, ErrWrongCode) {
		t.Errorf("send %v, receive %v", r.sendErr, r.recvErr)
	}
	if r.sendWords != "" {
		t.Error("the sender was asked to confirm despite a wrong code")
	}
	if len(store.m) != 0 {
		t.Errorf("stored %v", store.m)
	}
}

func TestTransferNotConfirmed(t *testing.T) {
	store := &memStore{m: map[string]string{}}
	r := transfer(t, sameNet(winbox, owner), sameNet(owner, winbox), store, keep, false, false)
	if !errors.Is(r.sendErr, ErrNotConfirmed) || r.recvErr == nil || len(store.m) != 0 {
		t.Errorf("send %v, receive %v, store %v", r.sendErr, r.recvErr, store.m)
	}
}

func TestTransferRefusesAnotherOwner(t *testing.T) {
	store := &memStore{m: map[string]string{}}
	// The receiver sees a sender of someone else's.
	r := transfer(t, sameNet(winbox, strange), sameNet(owner, winbox), store, keep, true, false)
	if r.recvErr == nil || !strings.Contains(r.recvErr.Error(), "not a device of this machine's owner") || r.sendErr == nil || len(store.m) != 0 {
		t.Errorf("receive %v, send %v", r.recvErr, r.sendErr)
	}
	// The sender sees a receiver of someone else's, and sends nothing.
	r = transfer(t, sameNet(winbox, owner), sameNet(owner, strange), store, keep, true, false)
	if r.sendErr == nil || !strings.Contains(r.sendErr.Error(), "not a device") || r.sendWords != "" || len(store.m) != 0 {
		t.Errorf("send %v", r.sendErr)
	}
}

// A party in the middle that knows the code (the worst case) and speaks
// to each side with a key of its own makes the two sides show different
// words; the person refuses, and nothing is sent.
func TestTransferMITMShowsDifferentWords(t *testing.T) {
	store := &memStore{m: map[string]string{}}
	snet := sameNet(owner, winbox)
	var code string
	got := make(chan string, 1)
	snet.dial = func(ctx context.Context, addr string) (net.Conn, error) {
		up, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		down, mine := net.Pipe()
		go mitm(t, up, mine, code, got)
		return pipeConn{down}, nil
	}
	r := transfer(t, sameNet(winbox, owner), snet, store, func(c string) string { code = c; return c }, false, false)
	if r.recvWords == "" || r.sendWords == "" {
		t.Fatalf("no words: send %v, receive %v", r.sendErr, r.recvErr)
	}
	if r.sendWords == r.recvWords {
		t.Errorf("the same words on both sides through a party in the middle: %q", r.sendWords)
	}
	if !errors.Is(r.sendErr, ErrNotConfirmed) || len(store.m) != 0 {
		t.Errorf("send %v, store %v", r.sendErr, store.m)
	}
	select {
	case m := <-got:
		t.Errorf("the party in the middle got %s", m)
	default:
	}
}

// pipeConn is a net.Pipe end with a TCP-looking remote address.
type pipeConn struct{ net.Conn }

func (pipeConn) RemoteAddr() net.Addr { return &net.TCPAddr{IP: net.IPv4(100, 64, 0, 9), Port: 1} }

// mitm talks to the receiver (up) as a sender and to the sender (down) as
// a receiver, each with a key of its own, and reports a bundle on got.
func mitm(t *testing.T, up, down net.Conn, code string, got chan<- string) {
	defer up.Close()
	defer down.Close()
	ur, dr := bufio.NewScanner(up), bufio.NewScanner(down)
	write := func(c net.Conn, m message) {
		m.V = Version
		b, _ := json.Marshal(m)
		_, _ = c.Write(append(b, '\n'))
	}
	read := func(sc *bufio.Scanner) message {
		var m message
		if sc.Scan() {
			_ = json.Unmarshal(sc.Bytes(), &m)
		}
		return m
	}
	k1, _ := age.GenerateX25519Identity()
	k2, _ := age.GenerateX25519Identity()
	recvHello := read(ur)
	write(down, message{Type: msgHello, Pub: k1.Recipient().String()})
	sendHello := read(dr)
	write(up, message{Type: msgHello, Pub: k2.Recipient().String(), Proof: proof(code, recvHello.Pub, k2.Recipient().String())})
	if read(ur).Type != msgReady {
		t.Error("the receiver did not take the proof of a party that knows the code")
		return
	}
	_ = sendHello
	write(down, message{Type: msgReady})
	if m := read(dr); m.Type == msgBundle {
		got <- m.Type
	}
}

func TestTransferTimeout(t *testing.T) {
	store := &memStore{m: map[string]string{}}
	_, err := Receive(context.Background(), ReceiveOptions{
		Net: sameNet(winbox, owner), Store: store, Allowed: allowed, Timeout: 200 * time.Millisecond,
	})
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("no sender: %v", err)
	}
	// A sender that connects and never says anything.
	ready := make(chan string, 1)
	errc := make(chan error, 1)
	go func() {
		_, err := Receive(context.Background(), ReceiveOptions{
			Net: sameNet(winbox, owner), Store: store, Allowed: allowed, Timeout: 300 * time.Millisecond,
			Ready: func(addr, _ string) { ready <- addr },
		})
		errc <- err
	}()
	c, err := net.Dial("tcp", <-ready)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := <-errc; err == nil || !errors.Is(err, ErrTimeout) {
		t.Errorf("a silent sender: %v", err)
	}
}

func TestTransferRollsBackAPartialWrite(t *testing.T) {
	store := &memStore{m: map[string]string{hostsecrets.AgeIdentity: "AGE-SECRET-KEY-1OLD"}, failOn: hostsecrets.B2VaultKeyID}
	r := transfer(t, sameNet(winbox, owner), sameNet(owner, winbox), store, keep, true, true)
	if r.recvErr == nil || r.sendErr == nil || !strings.Contains(r.recvErr.Error(), "put back") {
		t.Fatalf("receive %v, send %v", r.recvErr, r.sendErr)
	}
	if len(store.m) != 1 || store.m[hostsecrets.AgeIdentity] != "AGE-SECRET-KEY-1OLD" {
		t.Errorf("after the rollback: %v", store.m)
	}
}

func TestTransferRefusesToReplaceWithoutReplace(t *testing.T) {
	store := &memStore{m: map[string]string{hostsecrets.AgeIdentity: "AGE-SECRET-KEY-1OTHER"}}
	r := transfer(t, sameNet(winbox, owner), sameNet(owner, winbox), store, keep, true, false)
	if r.recvErr == nil || !strings.Contains(r.recvErr.Error(), "--replace") || store.m[hostsecrets.AgeIdentity] != "AGE-SECRET-KEY-1OTHER" || len(store.m) != 1 {
		t.Errorf("receive %v, store %v", r.recvErr, store.m)
	}
}

func TestTransferRefusesNamesNotAllowed(t *testing.T) {
	store := &memStore{m: map[string]string{}}
	rnet := sameNet(winbox, owner)
	ctx := context.Background()
	ready := make(chan [2]string, 1)
	errc := make(chan error, 1)
	go func() {
		_, err := Receive(ctx, ReceiveOptions{Net: rnet, Store: store, Allowed: []string{hostsecrets.AgeIdentity}, Timeout: 5 * time.Second,
			Ready: func(a, c string) { ready <- [2]string{a, c} }})
		errc <- err
	}()
	rc := <-ready
	_, err := Send(ctx, SendOptions{Net: sameNet(owner, winbox), To: rc[0], Code: rc[1], Secrets: sent, Timeout: 5 * time.Second,
		Confirm: func(Peer, string) (bool, error) { return true, nil }})
	if rerr := <-errc; rerr == nil || !strings.Contains(rerr.Error(), "does not take") || err == nil || len(store.m) != 0 {
		t.Errorf("receive %v, send %v, store %v", rerr, err, store.m)
	}
}

func TestSameOwner(t *testing.T) {
	if SameOwner(owner, winbox) != nil || SameOwner(owner, strange) == nil {
		t.Error("by login")
	}
	tagged := Peer{Login: "tagged-devices", Tags: []string{"tag:fridolin"}}
	if SameOwner(tagged, Peer{Login: "tagged-devices", Tags: []string{"tag:fridolin", "tag:x"}}) != nil {
		t.Error("a shared tag")
	}
	if SameOwner(tagged, Peer{Login: "tagged-devices", Tags: []string{"tag:other"}}) == nil {
		t.Error("tagged devices without a shared tag")
	}
	if SameOwner(Peer{}, Peer{}) == nil {
		t.Error("two devices without a login")
	}
}

func TestCodeAndWords(t *testing.T) {
	c, err := NewCode()
	if err != nil || len(c) != 9 || c[4] != '-' {
		t.Fatalf("%q %v", c, err)
	}
	if proof(c, "a", "b") != proof(strings.ToUpper(strings.ReplaceAll(c, "-", " ")), "a", "b") {
		t.Error("the code is not normalized")
	}
	if proof(c, "a", "b") == proof(c, "a", "c") {
		t.Error("the proof does not bind the keys")
	}
	if w := Words("a", "b"); w == Words("a", "c") || w != Words("a", "b") {
		t.Error("the words")
	}
	seen := map[string]bool{}
	for _, w := range wordList {
		if seen[w] {
			t.Errorf("%s twice", w)
		}
		seen[w] = true
	}
	if len(wordList) != 256 {
		t.Errorf("%d words", len(wordList))
	}
}

func TestDecodeBundle(t *testing.T) {
	good, _ := encodeBundle("mini", map[string]string{hostsecrets.AgeIdentity: "AGE-SECRET-KEY-1X"})
	if s, from, err := DecodeBundle(good); err != nil || from != "mini" || s[hostsecrets.AgeIdentity] != "AGE-SECRET-KEY-1X" {
		t.Fatalf("%v %q %v", s, from, err)
	}
	for name, in := range map[string]string{
		"empty":        `{"v":1,"from":"x","secrets":{}}`,
		"version":      `{"v":2,"from":"x","secrets":{"fridolin.a":"b"}}`,
		"a bad name":   `{"v":1,"from":"x","secrets":{"../a":"b"}}`,
		"not fridolin": `{"v":1,"from":"x","secrets":{"other.a":"b"}}`,
		"a space":      `{"v":1,"from":"x","secrets":{"fridolin.a":"b c"}}`,
		"a newline":    `{"v":1,"from":"x","secrets":{"fridolin.a":"b\nc"}}`,
		"empty value":  `{"v":1,"from":"x","secrets":{"fridolin.a":""}}`,
		"unknown":      `{"v":1,"from":"x","secrets":{"fridolin.a":"b"},"x":1}`,
		"trailing":     `{"v":1,"from":"x","secrets":{"fridolin.a":"b"}} {}`,
		"not json":     `nope`,
	} {
		if _, _, err := DecodeBundle([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func FuzzDecodeBundle(f *testing.F) {
	good, _ := encodeBundle("mini", map[string]string{hostsecrets.AgeIdentity: "AGE-SECRET-KEY-1X", hostsecrets.ResticPassword: "pw"})
	f.Add(good)
	f.Add([]byte(`{"v":1,"from":"x","secrets":{"fridolin.a":"b"}}`))
	f.Add([]byte(`{"v":1,"secrets":{"fridolin.a":"\u0000"}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		s, from, err := DecodeBundle(data)
		if err != nil {
			return
		}
		if len(s) == 0 || len(s) > maxSecrets || len(from) > 255 {
			t.Fatalf("accepted %d secrets from %q", len(s), from)
		}
		for n, v := range s {
			if !secretName.MatchString(n) || !validValue(v) {
				t.Fatalf("accepted %q=%q", n, v)
			}
		}
	})
}

func FuzzDecodeMessage(f *testing.F) {
	f.Add([]byte(`{"v":1,"type":"hello","pub":"age1x","proof":"00"}`))
	f.Add([]byte(`{"v":1,"type":"done","stored":["fridolin.a"]}`))
	f.Add([]byte(`{"v":1,"type":"bundle","data":"AAAA"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := decodeMessage(data)
		if err == nil && (m.V != Version || m.Type == "") {
			t.Fatalf("accepted %+v", m)
		}
	})
}
