package nodeproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CoreHostCap (core-host) is a machine that may run the core one day
// (docs/NODES.md, "core-host"; #187): it answers core.check, and holds
// host secrets that come to it end to end from another core host
// (core.secrets.*). It is never exec: each method does one fixed thing
// from typed parameters.
//
// It needs two keys. The owner turns it on in a terminal on the machine
// (`fridolin node core-host on`, or `node join --core-host`), which
// writes core-host.json in the node's state dir; and approves it on the
// core, like every capability. The agent declares the SHA-256 of that
// file (Hash) and the device ids it trusts as targets (SendTo), and an
// approval covers that one hash: every change of the file (turning it on
// again, a new trusted target) waits for the owner's approval again,
// since something else on the machine (a FrIDE task) may have written it.
type CoreHostCap struct {
	// Box is set for a node in a box.
	Box bool `json:"box,omitempty"`
	// Hash is the SHA-256 of core-host.json as the agent reads it, hex.
	Hash string `json:"hash"`
	// SendTo are the device ids this node encrypts host secrets to, as
	// pinned on this machine with `fridolin node core-host trust`.
	SendTo []string `json:"send_to,omitempty"`
}

// The core-host methods. Each needs the core_host capability.
const (
	// MethodCoreCheck reports the machine's facts for a move: its
	// setup-machine --core checks, which host secrets it holds (names
	// only), disk, version, box (nothing → CoreCheckResult).
	MethodCoreCheck = "core.check"
	// MethodCoreSecretsOffer makes an ephemeral key for one move's
	// secrets and signs it with the device key (CoreSecretsOfferParams →
	// CoreSecretsOffer).
	MethodCoreSecretsOffer = "core.secrets.offer"
	// MethodCoreSecretsSend encrypts this machine's host secrets to an
	// offer, after checking its signature against the target's device
	// key as pinned here (CoreSecretsSendParams → CoreSecretsBundle).
	MethodCoreSecretsSend = "core.secrets.send"
	// MethodCoreSecretsReceive decrypts a bundle with the offer's key and
	// stores it (CoreSecretsReceiveParams → CoreSecretsReceived).
	MethodCoreSecretsReceive = "core.secrets.receive"
	// MethodCoreSecretsForget removes the host secrets this machine
	// received over the node channel (nothing → CoreSecretsForgotten).
	MethodCoreSecretsForget = "core.secrets.forget"
)

// CoreHostMethods lists the core-host methods.
var CoreHostMethods = append([]string{MethodCoreCheck, MethodCoreSecretsOffer, MethodCoreSecretsSend, MethodCoreSecretsReceive, MethodCoreSecretsForget}, CoreMoveMethods...)

// CoreSecretsMethods are the core-host methods that move or remove host
// secrets: a lockdown refuses them.
var CoreSecretsMethods = []string{MethodCoreSecretsOffer, MethodCoreSecretsSend, MethodCoreSecretsReceive, MethodCoreSecretsForget}

// Limits of the core-host family.
const (
	maxCoreHostPins = 16
	// CoreSecretsOfferTTL is how long an offer's key lives on the target.
	CoreSecretsOfferTTL = 10 * time.Minute
	// MaxCoreSecretsBundle bounds a bundle's ciphertext.
	MaxCoreSecretsBundle = 256 << 10
)

var (
	hashRE     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	deviceIDRE = regexp.MustCompile(`^[0-9a-f]{12}$`)
	moveIDRE   = regexp.MustCompile(`^m-[0-9]{8}-[0-9]{4}-[0-9a-f]{4,16}$`)
)

// CheckDeviceID reports what is wrong with a device id.
func CheckDeviceID(id string) error {
	if !deviceIDRE.MatchString(id) {
		return fmt.Errorf("%q is not a device id (12 hex digits)", id)
	}
	return nil
}

// CheckMoveID reports what is wrong with a move id (m-20261023-1403-7f2a).
func CheckMoveID(id string) error {
	if !moveIDRE.MatchString(id) {
		return fmt.Errorf("%q is not a move id (m-YYYYMMDD-HHMM-hex)", id)
	}
	return nil
}

// NewMoveID is a move id for now, with rnd (hex) for its last part.
func NewMoveID(now time.Time, rnd string) string {
	return "m-" + now.UTC().Format("20060102-1504") + "-" + rnd
}

// Check reports what is wrong with the capability.
func (c *CoreHostCap) Check() error {
	if c == nil {
		return nil
	}
	if !hashRE.MatchString(c.Hash) {
		return errors.New("core_host: hash is not a SHA-256 in hex")
	}
	if len(c.SendTo) > maxCoreHostPins {
		return fmt.Errorf("core_host: at most %d trusted targets", maxCoreHostPins)
	}
	for _, id := range c.SendTo {
		if err := CheckDeviceID(id); err != nil {
			return fmt.Errorf("core_host: send_to: %w", err)
		}
	}
	return nil
}

func (c *CoreHostCap) summary() string {
	var extra []string
	if c.Box {
		extra = append(extra, "box")
	}
	switch n := len(c.SendTo); n {
	case 0:
	case 1:
		extra = append(extra, "trusts 1 target")
	default:
		extra = append(extra, fmt.Sprintf("trusts %d targets", n))
	}
	if len(extra) == 0 {
		return "core-host"
	}
	return "core-host (" + strings.Join(extra, ", ") + ")"
}

// Fingerprint is how a person compares a device key: the first 128 bits
// of its SHA-256, in eight groups of four hex digits.
func Fingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	h := hex.EncodeToString(sum[:16])
	parts := make([]string, 0, 8)
	for i := 0; i < len(h); i += 4 {
		parts = append(parts, h[i:i+4])
	}
	return strings.Join(parts, " ")
}

// CoreHostApproval is what approving core-host means (#187 §1): every
// place that approves it says so.
const CoreHostApproval = "core-host lets fridolin move the core to this machine. The machine then holds every host secret: the B2 keys, the age identity and export key (all exports and the WhatsApp session), the Claude token. Approve only a machine you trust as much as the one that runs the core now."

// FrideApproval is what approving fride means (#187 §5, "Follow-up
// outside this design"): every place that approves it says so. FrIDE
// runs Claude Code agents on the node, whatever exec allows.
const FrideApproval = "fride lets fridolin hand coding tasks to FrIDE on this machine. FrIDE runs Claude Code agents there, and they can run anything this machine's user can (on Windows through WSL, PowerShell and C:\\ too), whatever exec allows or refuses: approving fride is approving arbitrary commands on this machine. Approve it only on a machine you would let fridolin do anything on."

// CoreCheckResult is core.check's answer: facts, never a secret's value.
type CoreCheckResult struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Box     bool   `json:"box"`
	// Fingerprint is the device key's (Fingerprint), PublicKey the key,
	// hex, which `fridolin node core-host trust` pins on a source.
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"public_key"`
	// DiskFree is the bytes free where the agent keeps its state.
	DiskFree uint64 `json:"disk_free,omitempty"`
	// SecretStore names where the machine keeps host secrets; Secrets is
	// each one a move carries, stored or not. SecretsError says why the
	// store could not be read (a Windows service, a locked keychain).
	SecretStore  string              `json:"secret_store"`
	Secrets      []CoreSecretPresent `json:"secrets"`
	SecretsError string              `json:"secrets_error,omitempty"`
	// SecretFiles are the secret files of the core's credentials folder
	// a move carries, by name, and SecretFilesDir that folder under ~;
	// SecretFilesError says why they cannot be read (#187).
	SecretFiles      []string `json:"secret_files,omitempty"`
	SecretFilesDir   string   `json:"secret_files_dir,omitempty"`
	SecretFilesError string   `json:"secret_files_error,omitempty"`
	// SendTo are the targets this machine trusts, with their fingerprints.
	SendTo []CoreHostPinView `json:"send_to"`
	// Setup is `fridolin setup-machine --core` here; SetupNote says why
	// it was not run (a box: Docker is the box host's).
	Setup     []CoreCheckItem `json:"setup,omitempty"`
	SetupOK   bool            `json:"setup_ok"`
	SetupNote string          `json:"setup_note,omitempty"`
	// Core is the core on this machine as the lease and the bucket see
	// it; CoreError says why it could not be read (no store, no Docker,
	// a box: #191 phase 4).
	Core      *CoreRuntime `json:"core,omitempty"`
	CoreError string       `json:"core_error,omitempty"`
	// MachineBound is what of this machine a core that moves away leaves
	// behind (#187 §4): a fixed scan, report only.
	MachineBound []string `json:"machine_bound,omitempty"`
}

// CoreSecretPresent is one host secret by name, never by value.
type CoreSecretPresent struct {
	Name   string `json:"name"`
	Stored bool   `json:"stored"`
	// Received is set for one this machine received over the node
	// channel, which core.secrets.forget removes.
	Received bool `json:"received,omitempty"`
}

// CoreHostPinView is one trusted target.
type CoreHostPinView struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Fingerprint string `json:"fingerprint"`
}

// CoreCheckItem is one check of setup-machine --core.
type CoreCheckItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// CoreSecretsOfferParams asks a target for a key for Move's secrets.
type CoreSecretsOfferParams struct {
	Move string `json:"move"`
}

// CoreSecretsOffer is a target's ephemeral key for one move: an age
// recipient (ML-KEM-768 + X25519), signed with the target's device key
// over CoreSecretsOfferMessage.
type CoreSecretsOffer struct {
	Move      string    `json:"move"`
	Target    string    `json:"target"`
	Recipient string    `json:"recipient"`
	Expires   time.Time `json:"expires"`
	Signature []byte    `json:"signature"`
}

// Check reports what is wrong with the offer's form.
func (o CoreSecretsOffer) Check() error {
	if err := CheckMoveID(o.Move); err != nil {
		return err
	}
	if err := CheckDeviceID(o.Target); err != nil {
		return err
	}
	if o.Recipient == "" || len(o.Recipient) > 4096 || !strings.HasPrefix(o.Recipient, "age1pq1") {
		return errors.New("the offer's key is not a post-quantum age recipient")
	}
	if len(o.Signature) != ed25519.SignatureSize {
		return errors.New("the offer is not signed")
	}
	return nil
}

// CoreSecretsOfferMessage is what a target signs: the protocol, the move,
// its own device id, the key and when it expires (Unix seconds).
func CoreSecretsOfferMessage(move, target, recipient string, expires time.Time) []byte {
	return []byte("fridolin-core-secrets-offer/v1\x00" + move + "\x00" + target + "\x00" + recipient + "\x00" + strconv.FormatInt(expires.Unix(), 10))
}

// SignCoreSecretsOffer signs an offer with the target's device key.
func SignCoreSecretsOffer(key ed25519.PrivateKey, o CoreSecretsOffer) []byte {
	return ed25519.Sign(key, CoreSecretsOfferMessage(o.Move, o.Target, o.Recipient, o.Expires))
}

// VerifyCoreSecretsOffer checks an offer's signature with pub, the
// device key the source pinned for o.Target.
func VerifyCoreSecretsOffer(pub []byte, o CoreSecretsOffer) bool {
	if len(pub) != ed25519.PublicKeySize || DeviceIDFor(pub) != o.Target {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), CoreSecretsOfferMessage(o.Move, o.Target, o.Recipient, o.Expires), o.Signature)
}

// CoreSecretsSendParams hands a source a target's offer.
type CoreSecretsSendParams struct {
	Offer CoreSecretsOffer `json:"offer"`
}

// CoreSecretsBundle is a source's answer: its host secrets encrypted to
// the offer's key, which the core relays without being able to read.
// Names are the secrets inside, Missing those the source does not have.
type CoreSecretsBundle struct {
	Move       string   `json:"move"`
	From       string   `json:"from"`
	Ciphertext []byte   `json:"ciphertext"`
	Names      []string `json:"names"`
	Missing    []string `json:"missing,omitempty"`
	// Files are the secret files of the core's credentials folder sealed
	// beside them, by name (#187); FilesError says why none could be read.
	Files      []string `json:"files,omitempty"`
	FilesError string   `json:"files_error,omitempty"`
}

// CoreSecretsReceiveParams hands a target the bundle of its offer.
// Replace lets a value replace a different one already stored there.
type CoreSecretsReceiveParams struct {
	Move       string `json:"move"`
	From       string `json:"from"`
	Ciphertext []byte `json:"ciphertext"`
	Replace    bool   `json:"replace,omitempty"`
}

// CoreSecretsReceived is what a target stored: Stored were written,
// Unchanged were there with the same value.
type CoreSecretsReceived struct {
	Move      string   `json:"move"`
	Stored    []string `json:"stored"`
	Unchanged []string `json:"unchanged"`
	// FilesStored were written into the core's credentials folder,
	// FilesKept were there already and stay as they were.
	FilesStored []string `json:"files_stored,omitempty"`
	FilesKept   []string `json:"files_kept,omitempty"`
}

// CoreSecretsForgotten are the secrets a target removed.
type CoreSecretsForgotten struct {
	Forgotten []string `json:"forgotten"`
}
