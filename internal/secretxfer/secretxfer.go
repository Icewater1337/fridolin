// Package secretxfer carries the host secrets of the core (the age
// identity, the Backblaze keys, the vault and restic passwords, the
// Claude token) from the machine that runs the core to the machine that
// takes it over, directly over the tailnet (docs/CORE.md, "Moving the
// core to another machine").
//
// The receiver (`fridolin core receive`) makes an ephemeral age X25519
// key, listens once on its Tailscale address, and shows a one-time code.
// The sender (`fridolin core send`) connects, and both check with
// Tailscale (WhoIs) that the other is a device of the same owner. The
// sender proves it knows the code without sending it (an HMAC over both
// public keys); both show four words derived from both public keys,
// which the person compares; only then does the sender encrypt the
// secrets to the receiver's key and send them. The receiver decrypts
// them in memory and writes each into its host's secret store, putting
// back what was there when a write fails. Nothing is written to disk in
// clear and no value is printed. Every failure ends the transfer: the
// receiver takes one connection only.
package secretxfer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
)

// Version is the protocol's version.
const Version = 1

// DefaultTimeout is how long a receiver waits, and how long a transfer
// may take in all.
const DefaultTimeout = 10 * time.Minute

// Limits of what either side reads.
const (
	maxMessage    = 256 << 10
	maxSecrets    = 32
	maxValueBytes = 2560 // the Windows Credential Manager's limit
)

// Peer is a tailnet device as Tailscale names it.
type Peer struct {
	// Login is the owner's login (you@example.com); for a tagged device,
	// Tailscale's tagged-devices login.
	Login string
	// Node is the device's MagicDNS name.
	Node string
	Tags []string
}

func (p Peer) String() string {
	s := p.Node
	if p.Login != "" {
		s += " (" + p.Login + ")"
	}
	if len(p.Tags) > 0 {
		s += " [" + strings.Join(p.Tags, ",") + "]"
	}
	return s
}

// Tailnet is what a transfer needs of Tailscale on this machine; Local is
// the real one, tests use loopback.
type Tailnet interface {
	// Self is this device.
	Self(ctx context.Context) (Peer, error)
	// Listen opens a listener on this device's Tailscale address and
	// returns the address a sender dials.
	Listen(ctx context.Context) (net.Listener, string, error)
	Dial(ctx context.Context, addr string) (net.Conn, error)
	// WhoIs names the device behind a remote address.
	WhoIs(ctx context.Context, remoteAddr string) (Peer, error)
}

// SameOwner reports whether peer belongs to the owner of self: the same
// login, or, for tagged devices, a tag in common.
func SameOwner(self, peer Peer) error {
	if len(self.Tags) == 0 && len(peer.Tags) == 0 && self.Login != "" && peer.Login == self.Login {
		return nil
	}
	for _, t := range self.Tags {
		if slices.Contains(peer.Tags, t) {
			return nil
		}
	}
	return fmt.Errorf("%s is not a device of this machine's owner (%s); refusing", peer, self)
}

// Store is where the receiver writes the secrets: hostsecrets.Store.
type Store interface {
	Get(name string) (string, error)
	Set(name, value string) error
	Has(name string) (bool, error)
}

// deleter is a Store that can remove a secret, which a rollback uses for
// one that was not there before.
type deleter interface{ Delete(name string) error }

// Errors either side ends with.
var (
	ErrWrongCode    = errors.New("the code does not match: start fridolin core receive again and use its new code")
	ErrNotConfirmed = errors.New("the words were not confirmed; nothing was sent")
	ErrTimeout      = errors.New("timed out")
)

// message is one line of the protocol: JSON, one object per line.
type message struct {
	V       int      `json:"v"`
	Type    string   `json:"type"`
	Pub     string   `json:"pub,omitempty"`
	Proof   string   `json:"proof,omitempty"`
	Data    string   `json:"data,omitempty"`
	Stored  []string `json:"stored,omitempty"`
	Same    []string `json:"unchanged,omitempty"`
	Message string   `json:"message,omitempty"`
}

const (
	msgHello  = "hello"
	msgReady  = "ready"
	msgBundle = "bundle"
	msgDone   = "done"
	msgError  = "error"
	msgAbort  = "abort"
)

// decodeMessage reads one protocol line.
func decodeMessage(line []byte) (message, error) {
	var m message
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("a malformed message: %w", err)
	}
	if dec.More() {
		return m, errors.New("a malformed message: more than one object")
	}
	if m.V != Version {
		return m, fmt.Errorf("protocol version %d, this fridolin speaks %d: run the same version on both machines", m.V, Version)
	}
	switch m.Type {
	case msgHello, msgReady, msgBundle, msgDone, msgError, msgAbort:
	default:
		return m, fmt.Errorf("an unknown message %q", m.Type)
	}
	return m, nil
}

// conn is one side's end of the connection.
type conn struct {
	c  net.Conn
	sc *bufio.Scanner
}

func newConn(c net.Conn) *conn {
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 0, 4096), maxMessage)
	return &conn{c: c, sc: sc}
}

func (c *conn) send(m message) error {
	m.V = Version
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = c.c.Write(append(b, '\n'))
	return err
}

// read reads the next message; an error message from the other side
// becomes the error.
func (c *conn) read(want string) (message, error) {
	if !c.sc.Scan() {
		err := c.sc.Err()
		if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
			return message{}, ErrTimeout
		}
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return message{}, fmt.Errorf("the other side closed the connection: %w", err)
	}
	m, err := decodeMessage(c.sc.Bytes())
	if err != nil {
		return m, err
	}
	switch {
	case m.Type == msgError:
		return m, fmt.Errorf("the other side: %s", m.Message)
	case m.Type == msgAbort && want != msgAbort:
		return m, errors.New("the other side gave up: " + strings.TrimSpace(m.Message+" "+ErrNotConfirmed.Error()))
	case m.Type != want:
		return m, fmt.Errorf("expected %s, got %s", want, m.Type)
	}
	return m, nil
}

func (c *conn) fail(err error) error {
	_ = c.send(message{Type: msgError, Message: err.Error()})
	return err
}

// ── the code ──────────────────────────────────────────────────────────────

// codeAlphabet leaves out 0, 1, i, l and o, which read alike.
const codeAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"

// NewCode makes a one-time code: eight characters (about 40 bits), shown
// as two groups of four.
func NewCode() (string, error) {
	var b strings.Builder
	for i := range 8 {
		if i == 4 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
		if err != nil {
			return "", err
		}
		b.WriteByte(codeAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// normalizeCode drops what a person may add or change when typing it.
func normalizeCode(code string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '-' || r == ' ':
			return -1
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		}
		return r
	}, code)
}

// codeDomain separates the proof from any other use of the code.
const codeDomain = "fridolin secrets code v1"

// proof is the sender's proof that it knows the code, bound to both keys.
func proof(code, receiverPub, senderPub string) string {
	m := hmac.New(sha256.New, []byte(normalizeCode(code)))
	m.Write([]byte(codeDomain + "|" + receiverPub + "|" + senderPub))
	return hex.EncodeToString(m.Sum(nil))
}

// ── the bundle ────────────────────────────────────────────────────────────

// bundle is what the sender encrypts.
type bundle struct {
	V       int               `json:"v"`
	From    string            `json:"from"`
	Secrets map[string]string `json:"secrets"`
}

var secretName = regexp.MustCompile(`^fridolin\.[a-z0-9][a-z0-9._-]*$`)

// validValue: the secrets are tokens, keys, passwords and age identities,
// printable ASCII without spaces.
func validValue(v string) bool {
	if v == "" || len(v) > maxValueBytes {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x21 || v[i] > 0x7e {
			return false
		}
	}
	return true
}

// DecodeBundle reads a decrypted bundle, refusing anything but secret
// names with printable values.
func DecodeBundle(data []byte) (map[string]string, string, error) {
	var b bundle
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return nil, "", fmt.Errorf("a malformed bundle: %w", err)
	}
	if dec.More() {
		return nil, "", errors.New("a malformed bundle: trailing data")
	}
	switch {
	case b.V != Version:
		return nil, "", fmt.Errorf("bundle version %d", b.V)
	case len(b.Secrets) == 0:
		return nil, "", errors.New("the bundle holds no secrets")
	case len(b.Secrets) > maxSecrets:
		return nil, "", fmt.Errorf("the bundle holds %d secrets, more than %d", len(b.Secrets), maxSecrets)
	case len(b.From) > 255:
		return nil, "", errors.New("the bundle's sender name is too long")
	}
	for name, v := range b.Secrets {
		if !secretName.MatchString(name) || len(name) > 128 {
			return nil, "", fmt.Errorf("the bundle names %q, which is no secret name", name)
		}
		if !validValue(v) {
			return nil, "", fmt.Errorf("the bundle's value for %s is empty, too long or not printable", name)
		}
	}
	return b.Secrets, b.From, nil
}

func encodeBundle(from string, secrets map[string]string) ([]byte, error) {
	return json.Marshal(bundle{V: Version, From: from, Secrets: secrets})
}

// ── receiving ─────────────────────────────────────────────────────────────

// ReceiveOptions are the receiver's.
type ReceiveOptions struct {
	Net   Tailnet
	Store Store
	// Allowed are the names the receiver takes; a bundle with another is
	// refused whole.
	Allowed []string
	// Replace lets a received value replace a different one already in
	// the store; without it the transfer is refused before anything is
	// written.
	Replace bool
	// Timeout bounds the wait and the transfer (DefaultTimeout).
	Timeout time.Duration
	// Ready is called once the receiver listens, with the address and the
	// code to give the sender.
	Ready func(addr, code string)
	// Connected is called with the sender and the four words once the
	// sender proved the code.
	Connected func(peer Peer, words string)
}

// Result is what a transfer did.
type Result struct {
	Peer Peer
	// From is the host name the sender gave.
	From string
	// Stored were written; Unchanged were there with the same value.
	Stored, Unchanged []string
}

// Receive waits for one sender and stores what it sends.
func Receive(ctx context.Context, o ReceiveOptions) (Result, error) {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	self, err := o.Net.Self(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("asking Tailscale about this device: %w", err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return Result{}, err
	}
	code, err := NewCode()
	if err != nil {
		return Result{}, err
	}
	ln, addr, err := o.Net.Listen(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("listening on the tailnet: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	if o.Ready != nil {
		o.Ready(addr, code)
	}
	nc, err := ln.Accept()
	ln.Close() // one connection only
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("no sender within %s: %w", timeout, ErrTimeout)
		}
		return Result{}, err
	}
	defer nc.Close()
	dl, _ := ctx.Deadline()
	_ = nc.SetDeadline(dl)
	c := newConn(nc)
	peer, err := o.Net.WhoIs(ctx, nc.RemoteAddr().String())
	if err != nil {
		return Result{}, c.fail(fmt.Errorf("no device of the tailnet behind the connection (%s): %w", nc.RemoteAddr(), err))
	}
	res := Result{Peer: peer}
	if err := SameOwner(self, peer); err != nil {
		return res, c.fail(err)
	}
	recvPub := id.Recipient().String()
	if err := c.send(message{Type: msgHello, Pub: recvPub}); err != nil {
		return res, err
	}
	hello, err := c.read(msgHello)
	if err != nil {
		return res, err
	}
	if _, err := age.ParseX25519Recipient(hello.Pub); err != nil {
		return res, c.fail(errors.New("the sender's key is not an age key"))
	}
	if !hmac.Equal([]byte(hello.Proof), []byte(proof(code, recvPub, hello.Pub))) {
		return res, c.fail(ErrWrongCode)
	}
	if err := c.send(message{Type: msgReady}); err != nil {
		return res, err
	}
	if o.Connected != nil {
		o.Connected(peer, Words(recvPub, hello.Pub))
	}
	m, err := c.read(msgBundle)
	if err != nil {
		if errors.Is(err, ErrTimeout) {
			return res, fmt.Errorf("the sender did not confirm the words in time: %w", ErrTimeout)
		}
		return res, err
	}
	ct, err := base64.StdEncoding.DecodeString(m.Data)
	if err != nil {
		return res, c.fail(errors.New("the bundle is not base64"))
	}
	r, err := age.Decrypt(bytes.NewReader(ct), id)
	if err != nil {
		return res, c.fail(fmt.Errorf("decrypting the bundle: %w", err))
	}
	plain, err := io.ReadAll(io.LimitReader(r, maxMessage))
	if err != nil {
		return res, c.fail(fmt.Errorf("decrypting the bundle: %w", err))
	}
	secrets, from, err := DecodeBundle(plain)
	clear(plain)
	if err != nil {
		return res, c.fail(err)
	}
	res.From = from
	for name := range secrets {
		if !slices.Contains(o.Allowed, name) {
			return res, c.fail(fmt.Errorf("the bundle holds %s, which this machine does not take", name))
		}
	}
	res.Stored, res.Unchanged, err = storeAll(o.Store, secrets, o.Replace)
	if err != nil {
		return res, c.fail(err)
	}
	_ = c.send(message{Type: msgDone, Stored: res.Stored, Same: res.Unchanged})
	return res, nil
}

// storeAll writes the secrets, in name order, after checking that none
// would replace a different value (unless replace). When a write fails it
// puts back what each earlier one replaced.
func storeAll(st Store, secrets map[string]string, replace bool) (stored, unchanged []string, err error) {
	type before struct {
		value string
		had   bool
	}
	names := make([]string, 0, len(secrets))
	for n := range secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	prev := map[string]before{}
	var differ []string
	for _, n := range names {
		v, err := st.Get(n)
		switch {
		case err == nil:
			prev[n] = before{v, true}
			if v != secrets[n] {
				differ = append(differ, n)
			}
		case errors.Is(err, hostsecrets.ErrNotFound):
			prev[n] = before{}
		default:
			return nil, nil, fmt.Errorf("reading %s on this machine: %w", n, err)
		}
	}
	if len(differ) > 0 && !replace {
		return nil, nil, fmt.Errorf("this machine already holds other values for %s: nothing was stored; rerun fridolin core receive with --replace to replace them", strings.Join(differ, ", "))
	}
	var written []string
	for _, n := range names {
		if p := prev[n]; p.had && p.value == secrets[n] {
			unchanged = append(unchanged, n)
			continue
		}
		if err := st.Set(n, secrets[n]); err != nil {
			rb := rollback(st, written, func(name string) (string, bool) { return prev[name].value, prev[name].had })
			return nil, nil, fmt.Errorf("storing %s: %w; %s", n, err, rb)
		}
		written = append(written, n)
	}
	return written, unchanged, nil
}

// rollback puts back the earlier values of written, and says how it went.
func rollback(st Store, written []string, prev func(string) (string, bool)) string {
	if len(written) == 0 {
		return "nothing else was stored"
	}
	var failed []string
	for _, n := range written {
		v, had := prev(n)
		var err error
		switch {
		case had:
			err = st.Set(n, v)
		default:
			if d, ok := st.(deleter); ok {
				err = d.Delete(n)
			} else {
				err = errors.New("this store cannot remove a secret")
			}
		}
		if err != nil {
			failed = append(failed, n)
		}
	}
	if len(failed) > 0 {
		return "and could not put back " + strings.Join(failed, ", ") + ": check them with fridolin core secrets"
	}
	return "what was stored before it was put back (" + strings.Join(written, ", ") + ")"
}

// ── sending ───────────────────────────────────────────────────────────────

// SendOptions are the sender's.
type SendOptions struct {
	Net Tailnet
	// To is the receiver's address, as core receive printed it.
	To   string
	Code string
	// Secrets are the values to send, by name.
	Secrets map[string]string
	// From is this host's name, for the receiver's log.
	From    string
	Timeout time.Duration
	// Confirm shows the person the four words and the receiver, and says
	// whether they match what the receiver shows.
	Confirm func(peer Peer, words string) (bool, error)
}

// Send sends the secrets to a receiver once the person confirmed the words.
func Send(ctx context.Context, o SendOptions) (Result, error) {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if len(o.Secrets) == 0 {
		return Result{}, errors.New("there are no secrets to send")
	}
	for name, v := range o.Secrets {
		if !secretName.MatchString(name) || !validValue(v) {
			return Result{}, fmt.Errorf("%s cannot be sent: its value is empty, too long or not printable", name)
		}
	}
	self, err := o.Net.Self(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("asking Tailscale about this device: %w", err)
	}
	nc, err := o.Net.Dial(ctx, o.To)
	if err != nil {
		return Result{}, fmt.Errorf("connecting to %s: %w", o.To, err)
	}
	defer nc.Close()
	stop := context.AfterFunc(ctx, func() { _ = nc.SetDeadline(time.Now()) })
	defer stop()
	dl, _ := ctx.Deadline()
	_ = nc.SetDeadline(dl)
	c := newConn(nc)
	peer, err := o.Net.WhoIs(ctx, nc.RemoteAddr().String())
	if err != nil {
		return Result{}, fmt.Errorf("no device of the tailnet at %s: %w", nc.RemoteAddr(), err)
	}
	res := Result{Peer: peer}
	if err := SameOwner(self, peer); err != nil {
		return res, err
	}
	hello, err := c.read(msgHello)
	if err != nil {
		return res, err
	}
	recipient, err := age.ParseX25519Recipient(hello.Pub)
	if err != nil {
		return res, c.fail(errors.New("the receiver's key is not an age key"))
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return res, err
	}
	sendPub := id.Recipient().String()
	if err := c.send(message{Type: msgHello, Pub: sendPub, Proof: proof(o.Code, hello.Pub, sendPub)}); err != nil {
		return res, err
	}
	if _, err := c.read(msgReady); err != nil {
		if strings.Contains(err.Error(), ErrWrongCode.Error()) {
			return res, ErrWrongCode
		}
		return res, err
	}
	ok, err := o.Confirm(peer, Words(hello.Pub, sendPub))
	if err != nil || !ok {
		_ = c.send(message{Type: msgAbort, Message: "the words were not confirmed on the sender"})
		if err != nil {
			return res, err
		}
		return res, ErrNotConfirmed
	}
	plain, err := encodeBundle(o.From, o.Secrets)
	if err != nil {
		return res, err
	}
	var ct bytes.Buffer
	w, err := age.Encrypt(&ct, recipient)
	if err != nil {
		return res, err
	}
	_, err = w.Write(plain)
	clear(plain)
	if err == nil {
		err = w.Close()
	}
	if err != nil {
		return res, err
	}
	if err := c.send(message{Type: msgBundle, Data: base64.StdEncoding.EncodeToString(ct.Bytes())}); err != nil {
		return res, err
	}
	done, err := c.read(msgDone)
	if err != nil {
		return res, err
	}
	res.Stored, res.Unchanged = done.Stored, done.Same
	return res, nil
}
