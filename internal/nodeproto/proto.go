// Package nodeproto is the protocol between the core and its node agents
// (docs/NODES.md): versioned JSON messages over one WebSocket that the
// node opens to the core. The node says who it is (hello), proves it holds
// its device key (challenge, auth), hears what it may do (welcome, state),
// reports its host (heartbeat) and answers the core's capability calls
// (request, response) until either side closes, the core with a reason
// (close) such as a revocation.
//
// The package is pure: it encodes, decodes and checks messages and
// capability declarations, and leaves sockets, files and processes to the
// core (internal/nodes) and the agent (internal/nodeagent).
package nodeproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Version is the protocol's version. Every message carries it, and a peer
// refuses a message of another version: a change that old peers would
// misread raises it.
const Version = 1

// Path is the core's WebSocket endpoint, and Subprotocol the WebSocket
// subprotocol both sides ask for.
const (
	Path        = "/v1/node"
	Subprotocol = "fridolin-node.v1"
)

// Message types.
const (
	// TypeHello is the node's first message: who it is and what it offers.
	TypeHello = "hello"
	// TypeChallenge is the core's answer to hello: a nonce to sign.
	TypeChallenge = "challenge"
	// TypeAuth is the node's signature over the challenge.
	TypeAuth = "auth"
	// TypeWelcome accepts the node and says what it may do (a State).
	TypeWelcome = "welcome"
	// TypeState says what the node may do after an approval changed it.
	TypeState = "state"
	// TypeHeartbeat is the node's periodic report of its host.
	TypeHeartbeat = "heartbeat"
	// TypeRequest is a capability call from the core, with an ID.
	TypeRequest = "request"
	// TypeResponse answers the request with the same ID.
	TypeResponse = "response"
	// TypeError is a protocol error that ends the connection.
	TypeError = "error"
	// TypeClose says why the sender closes the connection.
	TypeClose = "close"
	// TypeEvent is news the node reports on its own (an Event), such as
	// FrIDE's thread news.
	TypeEvent = "event"
	// TypeDeclare is the node's new declaration (a Declare), when what it
	// offers changed without a new connection.
	TypeDeclare = "declare"
)

var knownTypes = map[string]bool{
	TypeHello: true, TypeChallenge: true, TypeAuth: true, TypeWelcome: true, TypeState: true,
	TypeHeartbeat: true, TypeRequest: true, TypeResponse: true, TypeError: true, TypeClose: true,
	TypeEvent: true, TypeDeclare: true,
}

// Limits on what a message may hold.
const (
	// MaxPreAuthBytes bounds a message before the node proved its key.
	MaxPreAuthBytes = 256 << 10
	// MaxMessageBytes bounds every later message: a file read or written
	// travels inside one, base64-encoded.
	MaxMessageBytes = 48 << 20
	// MaxFileBytes is the most fs.read returns and fs.write takes.
	MaxFileBytes = 16 << 20
	// MaxNameLen bounds a node's display name, in characters.
	MaxNameLen = 64
	// MaxIDLen bounds a request ID.
	MaxIDLen = 64
)

// Envelope is one message on the wire.
type Envelope struct {
	V    int             `json:"v"`
	Type string          `json:"type"`
	ID   string          `json:"id,omitempty"`
	Body json.RawMessage `json:"body,omitempty"`
}

// Encode marshals a message of type typ, with id ("" for none) and body
// (nil for none).
func Encode(typ, id string, body any) ([]byte, error) {
	env := Envelope{V: Version, Type: typ, ID: id}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding %s: %w", typ, err)
		}
		env.Body = raw
	}
	return json.Marshal(env)
}

// ErrVersion is a message of another protocol version.
var ErrVersion = errors.New("another protocol version")

// Decode parses one message and checks its envelope: the version, a known
// type and a bounded ID. The body is left for DecodeBody.
func Decode(data []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Envelope{}, fmt.Errorf("not a protocol message: %w", err)
	}
	if env.V != Version {
		return Envelope{}, fmt.Errorf("%w: v%d, this side speaks v%d", ErrVersion, env.V, Version)
	}
	if !knownTypes[env.Type] {
		return Envelope{}, fmt.Errorf("unknown message type %q", env.Type)
	}
	if len(env.ID) > MaxIDLen || !utf8.ValidString(env.ID) {
		return Envelope{}, errors.New("request ID too long or not UTF-8")
	}
	if (env.Type == TypeRequest || env.Type == TypeResponse) && env.ID == "" {
		return Envelope{}, fmt.Errorf("a %s needs an ID", env.Type)
	}
	return env, nil
}

// DecodeBody unmarshals env's body into a T. A missing body is T's zero
// value.
func DecodeBody[T any](env Envelope) (T, error) {
	var v T
	if len(env.Body) == 0 || string(env.Body) == "null" {
		return v, nil
	}
	if err := json.Unmarshal(env.Body, &v); err != nil {
		return v, fmt.Errorf("%s body: %w", env.Type, err)
	}
	return v, nil
}

// Hello is the node's first message.
type Hello struct {
	// DeviceID is DeviceIDFor(PublicKey).
	DeviceID string `json:"device_id"`
	// PublicKey is the node's ed25519 device key.
	PublicKey []byte `json:"public_key"`
	Name      string `json:"name"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	// Version is the fridolin version the node runs.
	Version      string       `json:"version"`
	Capabilities Capabilities `json:"capabilities"`
	// JoinCode is the single-use code of `fridolin nodes join-code`, for a
	// device the core does not know yet; empty afterwards.
	JoinCode string `json:"join_code,omitempty"`
}

// Check reports what is wrong with h, nil when nothing is.
func (h Hello) Check() error {
	if len(h.PublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("public key: %d bytes, want %d", len(h.PublicKey), ed25519.PublicKeySize)
	}
	if h.DeviceID != DeviceIDFor(h.PublicKey) {
		return errors.New("the device ID is not the one of the public key")
	}
	if err := CheckName(h.Name); err != nil {
		return err
	}
	for field, v := range map[string]string{"os": h.OS, "arch": h.Arch, "version": h.Version} {
		if len(v) > 64 || !utf8.ValidString(v) || strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("%s: too long or not printable", field)
		}
	}
	if len(h.JoinCode) > 128 {
		return errors.New("join code too long")
	}
	return h.Capabilities.Check()
}

// CheckName reports whether name is a display name a node may have:
// 1 to MaxNameLen printable characters.
func CheckName(name string) error {
	if name == "" || utf8.RuneCountInString(name) > MaxNameLen || !utf8.ValidString(name) {
		return fmt.Errorf("name: 1 to %d characters", MaxNameLen)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return errors.New("name: no control characters")
		}
	}
	return nil
}

// DeviceIDFor is the ID of the device with key pub: the first 12 hex
// digits of its SHA-256. It names the node in the registry, the CLI and
// the dashboard, and since it follows from the key, a node cannot claim
// another's.
func DeviceIDFor(pub []byte) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:6])
}

// Challenge is the core's answer to hello.
type Challenge struct {
	Nonce []byte `json:"nonce"`
}

// NonceSize is the length of a challenge's nonce.
const NonceSize = 32

// Auth is the node's proof that it holds the key of its hello.
type Auth struct {
	Signature []byte `json:"signature"`
}

// AuthMessage is what the node signs: the protocol, its device ID and the
// nonce, so a signature answers one challenge of one device only.
func AuthMessage(deviceID string, nonce []byte) []byte {
	msg := []byte("fridolin-node-auth/v1\x00" + deviceID + "\x00")
	return append(msg, nonce...)
}

// Sign signs the challenge nonce for deviceID with key.
func Sign(key ed25519.PrivateKey, deviceID string, nonce []byte) []byte {
	return ed25519.Sign(key, AuthMessage(deviceID, nonce))
}

// Verify checks a signature of Sign.
func Verify(pub []byte, deviceID string, nonce, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize || len(nonce) != NonceSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), AuthMessage(deviceID, nonce), sig)
}

// The approval stages of a node (docs/NODES.md, pairing).
const (
	// DevicePending is a device that joined with a code and waits for
	// `fridolin nodes approve <id>`.
	DevicePending = "pending"
	// DeviceApproved is a device the owner approved.
	DeviceApproved = "approved"
	// DeviceRevoked is a device the owner revoked; it may not connect.
	DeviceRevoked = "revoked"

	// CommandsNone is a node that declares no capability.
	CommandsNone = "none"
	// CommandsPending is a declaration the owner has not approved, or one
	// that grew since the last approval.
	CommandsPending = "pending"
	// CommandsApproved is a declaration the approval covers.
	CommandsApproved = "approved"
)

// State is what the core lets the node do, in welcome and state.
type State struct {
	Device   string `json:"device"`
	Commands string `json:"commands"`
	// Approved is the part of the node's declaration it may serve: none
	// before the device and its commands are approved.
	Approved Capabilities `json:"approved"`
	// Message says, for a person reading the agent's log, what is next.
	Message string `json:"message,omitempty"`
	// CoreKey is the core's ed25519 update key, which signs the binaries
	// it updates nodes to: a node pins it when it joins (UpdateCap).
	CoreKey []byte `json:"core_key,omitempty"`
}

// Heartbeat is the node's periodic report.
type Heartbeat struct {
	Stats HostStats `json:"stats"`
}

// HostStats are the node's free resources.
type HostStats struct {
	CPUs int `json:"cpus"`
	// MemTotal and MemAvailable are bytes; 0 where unknown.
	MemTotal     uint64 `json:"mem_total,omitempty"`
	MemAvailable uint64 `json:"mem_available,omitempty"`
	// DiskFree is the bytes free where the agent keeps its state.
	DiskFree uint64 `json:"disk_free,omitempty"`
	// Battery is the charge in percent, nil without a battery or where
	// reading it is not cheap.
	Battery *int `json:"battery,omitempty"`
}

// Request is a capability call.
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response answers a request: a result, or an error.
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error codes of a failed call.
const (
	// CodeDenied is a call the approvals or the node's allowlist refuse.
	CodeDenied = "denied"
	// CodeOutsideRoot is a path outside the granted roots.
	CodeOutsideRoot = "outside-root"
	// CodeNotFound is a path that is not there.
	CodeNotFound = "not-found"
	// CodeBadRequest is a call the node cannot read.
	CodeBadRequest = "bad-request"
	// CodeTimeout is a command or call that ran out of time.
	CodeTimeout = "timeout"
	// CodeFailed is a call that failed on the node.
	CodeFailed = "failed"
	// CodeUnavailable is a node that is not connected.
	CodeUnavailable = "unavailable"
	// CodeUnsupported is a method the node does not know.
	CodeUnsupported = "unsupported"
)

// Error is why a call failed, and the error of TypeError.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf builds an *Error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Close reasons.
const (
	// CloseRevoked is a node the owner revoked: it must not reconnect.
	CloseRevoked = "revoked"
	// CloseUnknownDevice is a device the core does not know, that came
	// without a valid join code.
	CloseUnknownDevice = "unknown-device"
	// CloseBadAuth is a signature that does not verify.
	CloseBadAuth = "bad-auth"
	// CloseReplaced is a connection a newer one of the same node replaced.
	CloseReplaced = "replaced"
	// CloseShutdown is the core or the agent stopping.
	CloseShutdown = "shutdown"
	// CloseProtocol is a message the other side could not take.
	CloseProtocol = "protocol"
	// CloseIdle is a connection that sent nothing for too long.
	CloseIdle = "idle"
)

// Close says why the sender closes the connection.
type Close struct {
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
}

// Final reports whether a node closed for reason should stop trying to
// reconnect: it was revoked, or the core does not know it.
func Final(reason string) bool {
	return reason == CloseRevoked || reason == CloseUnknownDevice
}

// The capability methods, by the family that grants them.
const (
	MethodExec     = "exec"
	MethodFSList   = "fs.list"
	MethodFSRead   = "fs.read"
	MethodFSWrite  = "fs.write"
	MethodHostInfo = "host.info"
	MethodNotify   = "notify"
)

// Methods lists every method.
var Methods = slices.Concat([]string{MethodExec, MethodExecPrograms, MethodFSList, MethodFSRead, MethodFSWrite, MethodHostInfo, MethodNotify}, FrideMethods, AndroidMethods, BackupMethods, UpdateMethods, AgentMethods, TerminalMethods, CoreHostMethods, SandboxMethods)

// ExecParams runs argv on the node.
type ExecParams struct {
	Argv []string `json:"argv"`
	// Dir is the working directory, "" for the agent's.
	Dir string `json:"dir,omitempty"`
	// TimeoutSeconds shortens the node's timeout, 0 for the node's.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// ExecResult is how a command ended.
type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	// Truncated says the output went past the node's cap and was cut.
	Truncated  bool  `json:"truncated,omitempty"`
	TimedOut   bool  `json:"timed_out,omitempty"`
	DurationMS int64 `json:"duration_ms"`
}

// PathParams names a path on the node: absolute, under a granted root.
type PathParams struct {
	Path string `json:"path"`
}

// FSEntry is one entry of a folder.
type FSEntry struct {
	Name    string    `json:"name"`
	Dir     bool      `json:"dir,omitempty"`
	Link    bool      `json:"link,omitempty"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// FSListResult is a folder's entries, sorted by name.
type FSListResult struct {
	Path    string    `json:"path"`
	Entries []FSEntry `json:"entries"`
}

// FSReadParams reads a file, at most MaxBytes of it (0 for MaxFileBytes).
type FSReadParams struct {
	Path     string `json:"path"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

// FSReadResult is a file's content.
type FSReadResult struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Data      []byte `json:"data"`
	Truncated bool   `json:"truncated,omitempty"`
}

// FSWriteParams writes a file whole, under a root granted for writing.
type FSWriteParams struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
	// Mkdirs creates the folders above it that are missing.
	Mkdirs bool `json:"mkdirs,omitempty"`
}

// FSWriteResult is the file written.
type FSWriteResult struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// HostInfo describes the node's host.
type HostInfo struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
	HostStats
}

// NotifyParams shows a notification on the node's desktop.
type NotifyParams struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

// NotifyResult says how the notification was shown.
type NotifyResult struct {
	// Shown is false where the node only logged it (no notifier
	// installed, or nobody signed in to Windows); Reason then says why.
	Shown  bool   `json:"shown"`
	Method string `json:"method"`
	Reason string `json:"reason,omitempty"`
}
