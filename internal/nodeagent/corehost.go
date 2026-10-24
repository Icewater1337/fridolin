package nodeagent

import (
	"context"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"filippo.io/age"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/secretxfer"
)

// core-host (docs/NODES.md, "core-host"; #187): a machine the core may
// move to. Its local settings, the opt-in and the targets this machine
// trusts, live in core-host.json in the state dir, not node.json: the
// core cannot change them, and the workstation's sandbox denies the
// state dir. The agent declares the file's hash, and the core's approval
// covers that one hash.
const (
	// CoreHostFile is the core-host settings, written only by `fridolin
	// node core-host` on this machine.
	CoreHostFile = "core-host.json"
	// coreSecretsDir is a box's secret store (hostsecrets.Sealed).
	coreSecretsDir = "core-secrets"
	// coreReceivedFile lists the secrets this machine received over the
	// node channel that it did not hold before: what core.secrets.forget
	// removes.
	coreReceivedFile = "core-secrets-received.json"
	// TransferLogFile gets a line, names only, for every transfer.
	TransferLogFile = "secrets-transfer.log"
	// maxCoreOffers is how many offers may wait at once.
	maxCoreOffers = 4
)

// CoreHostConfig is core-host.json.
type CoreHostConfig struct {
	// Enabled is the local opt-in: `fridolin node core-host on`.
	Enabled bool `json:"enabled"`
	// SendTo are the targets this machine encrypts host secrets to
	// (`fridolin node core-host trust ID --key KEY`).
	SendTo []CoreHostPin `json:"send_to,omitempty"`
}

// CoreHostPin is one trusted target: its device id and key, as the
// target's `fridolin node core-host show` prints them.
type CoreHostPin struct {
	ID string `json:"id"`
	// PublicKey is the device's ed25519 key, hex.
	PublicKey string    `json:"public_key"`
	Name      string    `json:"name,omitempty"`
	PinnedAt  time.Time `json:"pinned_at"`
}

// Key is the pin's public key.
func (p CoreHostPin) Key() (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(p.PublicKey)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("the key of %s is not an ed25519 public key in hex", p.ID)
	}
	return ed25519.PublicKey(b), nil
}

// Check reports what is wrong with the settings.
func (c CoreHostConfig) Check() error {
	if len(c.SendTo) > 16 {
		return errors.New("core-host: at most 16 trusted targets")
	}
	seen := map[string]bool{}
	for _, p := range c.SendTo {
		if err := nodeproto.CheckDeviceID(p.ID); err != nil {
			return fmt.Errorf("core-host: send_to: %w", err)
		}
		key, err := p.Key()
		if err != nil {
			return fmt.Errorf("core-host: %w", err)
		}
		if nodeproto.DeviceIDFor(key) != p.ID {
			return fmt.Errorf("core-host: the key pinned for %s is the key of %s", p.ID, nodeproto.DeviceIDFor(key))
		}
		if seen[p.ID] {
			return fmt.Errorf("core-host: %s is trusted twice", p.ID)
		}
		seen[p.ID] = true
		if len(p.Name) > 64 {
			return fmt.Errorf("core-host: the name of %s is too long", p.ID)
		}
	}
	return nil
}

// Hash is what the agent declares of the settings: the SHA-256 of their
// JSON as fridolin writes it.
func (c CoreHostConfig) Hash() string {
	data, _ := json.Marshal(c)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Pin is the pin of target id, if this machine trusts it.
func (c CoreHostConfig) Pin(id string) (CoreHostPin, bool) {
	i := slices.IndexFunc(c.SendTo, func(p CoreHostPin) bool { return p.ID == id })
	if i < 0 {
		return CoreHostPin{}, false
	}
	return c.SendTo[i], true
}

// LoadCoreHost reads dir's core-host.json; a missing file is core-host off.
func LoadCoreHost(dir string) (CoreHostConfig, error) {
	var c CoreHostConfig
	data, err := os.ReadFile(filepath.Join(dir, CoreHostFile))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("reading %s: %w", filepath.Join(dir, CoreHostFile), err)
	}
	return c, c.Check()
}

// SaveCoreHost writes dir's core-host.json.
func SaveCoreHost(dir string, c CoreHostConfig) error {
	if err := c.Check(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, CoreHostFile), append(data, '\n'), 0o600)
}

// coreHost is the agent's core-host state: the offers waiting for their
// bundle, each with its one-time key, in memory only.
type coreHost struct {
	mu       sync.Mutex
	offers   map[string]coreOffer
	declared string
}

type coreOffer struct {
	id      *age.HybridIdentity
	expires time.Time
}

// coreHostDeclared is the core_host the node declares now: nil unless
// core-host.json turns it on.
func (a *Agent) coreHostDeclared() *nodeproto.CoreHostCap {
	if a.Dir == "" {
		return nil
	}
	c, err := LoadCoreHost(a.Dir)
	if err != nil || !c.Enabled {
		return nil
	}
	ch := &nodeproto.CoreHostCap{Box: a.Config.Box != nil, Hash: c.Hash()}
	for _, p := range c.SendTo {
		ch.SendTo = append(ch.SendTo, p.ID)
	}
	return ch
}

// coreHostChanged reports whether core-host.json changed since the last
// time it asked, so that the agent declares it again.
func (a *Agent) coreHostChanged() bool {
	now := ""
	if ch := a.coreHostDeclared(); ch != nil {
		now = ch.Hash
	}
	a.core.mu.Lock()
	defer a.core.mu.Unlock()
	changed := a.core.declared != now
	a.core.declared = now
	return changed
}

// CoreSecretsKey is the key a box seals its host secrets with, derived
// from its device key: the volume holds them encrypted at rest.
func CoreSecretsKey(key ed25519.PrivateKey) ([]byte, error) {
	return hkdf.Key(sha256.New, key.Seed(), nil, "fridolin box host secrets v1", 32)
}

// coreStore is where this machine keeps host secrets: the box's sealed
// files, else the host's store (keychain, files). A Windows node runs
// as LocalSystem, whose Credential Manager is not the owner's, so it
// keeps none for now.
func (a *Agent) coreStore() (hostsecrets.Store, string, *nodeproto.Error) {
	if a.CoreSecrets != nil {
		place := a.CoreSecretsPlace
		if place == "" {
			place = "the node's secret store"
		}
		return a.CoreSecrets, place, nil
	}
	if a.Config.Box != nil {
		if a.Dir == "" {
			return nil, "", nodeproto.Errorf(nodeproto.CodeUnavailable, "this box has no state dir")
		}
		k, err := CoreSecretsKey(a.Key)
		if err != nil {
			return nil, "", nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
		}
		return hostsecrets.Sealed{Dir: filepath.Join(a.Dir, coreSecretsDir), Key: k}, "the box's volume (sealed)", nil
	}
	if runtime.GOOS == "windows" {
		return nil, "", nodeproto.Errorf(nodeproto.CodeUnavailable, "the node service runs as LocalSystem, whose Credential Manager is not the owner's: move the secrets with fridolin core send and receive")
	}
	return nil, "", nodeproto.Errorf(nodeproto.CodeUnavailable, "this agent was started without a secret store")
}

// HostSecretStore is the store a native agent on goos keeps host secrets
// in, as `fridolin core` does (none on Windows, see coreStore).
func HostSecretStore(goos string, getenv func(string) string, home string) (hostsecrets.Store, string) {
	if goos == "windows" {
		return nil, ""
	}
	return hostsecrets.ForHost(goos, getenv, home), hostsecrets.Place(goos, getenv, home)
}

func (a *Agent) coreHostCall(ctx context.Context, method string, params json.RawMessage) (any, *nodeproto.Error) {
	decode := func(v any) *nodeproto.Error {
		if len(params) == 0 {
			return nil
		}
		dec := json.NewDecoder(strings.NewReader(string(params)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			return nodeproto.Errorf(nodeproto.CodeBadRequest, "%s params: %v", method, err)
		}
		return nil
	}
	switch method {
	case nodeproto.MethodCoreCheck:
		return a.coreCheck(ctx), nil
	case nodeproto.MethodCoreSecretsOffer:
		var p nodeproto.CoreSecretsOfferParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.coreOffer(p)
	case nodeproto.MethodCoreSecretsSend:
		var p nodeproto.CoreSecretsSendParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.coreSend(p)
	case nodeproto.MethodCoreSecretsReceive:
		var p nodeproto.CoreSecretsReceiveParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.coreReceive(p)
	case nodeproto.MethodCoreSecretsForget:
		var p struct{}
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.coreForget()
	case nodeproto.MethodCoreImagePull:
		var p nodeproto.CoreImagePullParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.coreImage(ctx, p)
	case nodeproto.MethodCoreRehearse:
		var p nodeproto.CoreRehearseParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.coreRehearse(ctx, p)
	case nodeproto.MethodCoreRelease:
		return a.coreMoveAccept(params, nodeproto.MoveSource)
	case nodeproto.MethodCoreHandoff:
		return a.coreMoveAccept(params, nodeproto.MoveTarget)
	case nodeproto.MethodCoreMoveStatus:
		var p nodeproto.CoreMoveStatusParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.coreMoveStatus(p)
	}
	return nil, nodeproto.Errorf(nodeproto.CodeUnsupported, "no method %q", method)
}

// coreCheck is core.check: facts, never a value.
func (a *Agent) coreCheck(ctx context.Context) nodeproto.CoreCheckResult {
	pub := a.Key.Public().(ed25519.PublicKey)
	res := nodeproto.CoreCheckResult{
		ID: a.ID(), Version: a.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, Box: a.Config.Box != nil,
		Fingerprint: nodeproto.Fingerprint(pub), PublicKey: hex.EncodeToString(pub), SendTo: []nodeproto.CoreHostPinView{},
	}
	if a.Dir != "" {
		if free, err := platform.FreeBytes(a.Dir); err == nil {
			res.DiskFree = free
		}
		if c, err := LoadCoreHost(a.Dir); err == nil {
			for _, p := range c.SendTo {
				key, _ := p.Key()
				res.SendTo = append(res.SendTo, nodeproto.CoreHostPinView{ID: p.ID, Name: p.Name, Fingerprint: nodeproto.Fingerprint(key)})
			}
		}
	}
	received := a.coreReceived()
	st, place, perr := a.coreStore()
	res.SecretStore = place
	if perr != nil {
		res.SecretsError = perr.Message
	} else {
		for _, n := range secretxfer.MoveNames {
			ok, err := st.Has(n)
			if err != nil {
				res.SecretsError = err.Error()
				break
			}
			res.Secrets = append(res.Secrets, nodeproto.CoreSecretPresent{Name: n, Stored: ok, Received: ok && slices.Contains(received, n)})
		}
	}
	res.Setup, res.SetupOK, res.SetupNote = a.coreSetup(ctx)
	res.Core, res.CoreError = a.coreRuntime(ctx)
	res.MachineBound = a.machineBound()
	return res
}

// coreSetup runs `fridolin setup-machine --core --json` with the agent's
// own binary; a box leaves it out, since Docker is its host's.
func (a *Agent) coreSetup(ctx context.Context) ([]nodeproto.CoreCheckItem, bool, string) {
	if a.CoreSetup != nil {
		return a.CoreSetup(ctx)
	}
	if a.Config.Box != nil {
		return nil, true, "a box: the image, Docker and the volumes are the box's own"
	}
	if a.Executable == "" {
		return nil, false, "not run: the agent does not know its own binary"
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	// The agent's own binary with a fixed subcommand: nothing of the
	// call goes into it.
	out, err := exec.CommandContext(ctx, a.Executable, "setup-machine", "--core", "--json").Output()
	var rep struct {
		OK     bool                      `json:"ok"`
		Checks []nodeproto.CoreCheckItem `json:"checks"`
	}
	if jerr := json.Unmarshal(out, &rep); jerr != nil {
		if err == nil {
			err = jerr
		}
		return nil, false, "setup-machine --core: " + err.Error()
	}
	return rep.Checks, rep.OK, ""
}

// coreOffer is core.secrets.offer: a one-time key for one move, kept in
// memory until its bundle comes or it expires, signed with the device
// key so that the source can tell it is this machine's.
func (a *Agent) coreOffer(p nodeproto.CoreSecretsOfferParams) (any, *nodeproto.Error) {
	if err := nodeproto.CheckMoveID(p.Move); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	if _, _, perr := a.coreStore(); perr != nil {
		return nil, perr
	}
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	now := time.Now()
	o := nodeproto.CoreSecretsOffer{Move: p.Move, Target: a.ID(), Recipient: id.Recipient().String(), Expires: now.Add(nodeproto.CoreSecretsOfferTTL).Truncate(time.Second)}
	a.core.mu.Lock()
	if a.core.offers == nil {
		a.core.offers = map[string]coreOffer{}
	}
	for m, off := range a.core.offers {
		if now.After(off.expires) {
			delete(a.core.offers, m)
		}
	}
	_, dup := a.core.offers[p.Move]
	full := len(a.core.offers) >= maxCoreOffers
	if !dup && !full {
		a.core.offers[p.Move] = coreOffer{id: id, expires: o.Expires}
	}
	a.core.mu.Unlock()
	switch {
	case dup:
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "move %s already has an offer", p.Move)
	case full:
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "%d offers are waiting already", maxCoreOffers)
	}
	o.Signature = nodeproto.SignCoreSecretsOffer(a.Key, o)
	a.transferLog(map[string]any{"direction": "offered", "move": p.Move, "result": "ok"})
	return o, nil
}

// coreSend is core.secrets.send: the host secrets, encrypted to the
// offer's key once its signature verifies with the key this machine
// pinned for the target. The core, which relays the offer, cannot swap
// in a key of its own.
func (a *Agent) coreSend(p nodeproto.CoreSecretsSendParams) (any, *nodeproto.Error) {
	o := p.Offer
	fail := func(code, format string, args ...any) (any, *nodeproto.Error) {
		e := nodeproto.Errorf(code, format, args...)
		a.transferLog(map[string]any{"direction": "sent", "move": o.Move, "peer": o.Target, "result": "refused: " + e.Message})
		return nil, e
	}
	if err := o.Check(); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	if o.Target == a.ID() {
		return fail(nodeproto.CodeDenied, "the offer is this machine's own")
	}
	cfg, err := LoadCoreHost(a.Dir)
	if err != nil {
		return fail(nodeproto.CodeFailed, "%v", err)
	}
	pin, ok := cfg.Pin(o.Target)
	if !ok {
		return fail(nodeproto.CodeDenied, "this machine does not trust %s as a target: on this machine, fridolin node core-host trust %s --key KEY (the key fridolin node core-host show prints there)", o.Target, o.Target)
	}
	key, err := pin.Key()
	if err != nil {
		return fail(nodeproto.CodeFailed, "%v", err)
	}
	if !nodeproto.VerifyCoreSecretsOffer(key, o) {
		return fail(nodeproto.CodeDenied, "the offer of %s is not signed by the key pinned for it here (fingerprint %s): nothing was sent", o.Target, nodeproto.Fingerprint(key))
	}
	now := time.Now()
	if !now.Before(o.Expires) || o.Expires.After(now.Add(nodeproto.CoreSecretsOfferTTL+time.Minute)) {
		return fail(nodeproto.CodeDenied, "the offer expired, or expires too late")
	}
	st, _, perr := a.coreStore()
	if perr != nil {
		return fail(perr.Code, "%s", perr.Message)
	}
	secrets := map[string]string{}
	defer clear(secrets)
	b := nodeproto.CoreSecretsBundle{Move: o.Move, From: a.ID()}
	for _, n := range secretxfer.MoveNames {
		v, err := st.Get(n)
		switch {
		case errors.Is(err, hostsecrets.ErrNotFound):
			b.Missing = append(b.Missing, n)
		case err != nil:
			return fail(nodeproto.CodeFailed, "reading %s: %v", n, err)
		default:
			secrets[n] = v
			b.Names = append(b.Names, n)
		}
	}
	ct, err := secretxfer.SealBundle(a.Config.Name, secrets, o.Recipient)
	if err != nil {
		return fail(nodeproto.CodeFailed, "%v", err)
	}
	b.Ciphertext = ct
	a.transferLog(map[string]any{"direction": "sent", "move": o.Move, "peer": o.Target, "peer_name": pin.Name, "names": b.Names, "result": "ok"})
	a.logf("node: core-host: encrypted %d host secrets for %s (move %s)", len(b.Names), o.Target, o.Move)
	return b, nil
}

// coreReceive is core.secrets.receive: decrypted in memory with the
// move's one-time key, which goes with it, and written as core receive
// writes, refusing names a move does not carry.
func (a *Agent) coreReceive(p nodeproto.CoreSecretsReceiveParams) (any, *nodeproto.Error) {
	fail := func(code, format string, args ...any) (any, *nodeproto.Error) {
		e := nodeproto.Errorf(code, format, args...)
		a.transferLog(map[string]any{"direction": "received", "move": p.Move, "peer": p.From, "result": "error: " + e.Message})
		return nil, e
	}
	if err := nodeproto.CheckMoveID(p.Move); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	if len(p.Ciphertext) == 0 || len(p.Ciphertext) > nodeproto.MaxCoreSecretsBundle {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "the bundle is empty or too large")
	}
	a.core.mu.Lock()
	off, ok := a.core.offers[p.Move]
	delete(a.core.offers, p.Move) // one bundle per offer, whatever happens
	a.core.mu.Unlock()
	if !ok || time.Now().After(off.expires) {
		return fail(nodeproto.CodeDenied, "no offer for move %s waits here (or it expired): offer again", p.Move)
	}
	st, place, perr := a.coreStore()
	if perr != nil {
		return fail(perr.Code, "%s", perr.Message)
	}
	secrets, _, err := secretxfer.OpenBundle(p.Ciphertext, off.id)
	if err != nil {
		return fail(nodeproto.CodeDenied, "%v", err)
	}
	defer clear(secrets)
	var absent []string
	for n := range secrets {
		if ok, err := st.Has(n); err == nil && !ok {
			absent = append(absent, n)
		}
	}
	stored, unchanged, err := secretxfer.StoreBundle(st, secrets, secretxfer.MoveNames, p.Replace)
	if err != nil {
		return fail(nodeproto.CodeFailed, "%v", err)
	}
	var fresh []string
	for _, n := range stored {
		if slices.Contains(absent, n) {
			fresh = append(fresh, n)
		}
	}
	if err := a.noteReceived(fresh); err != nil {
		a.logf("node: core-host: noting the received secrets: %v", err)
	}
	a.transferLog(map[string]any{"direction": "received", "move": p.Move, "peer": p.From, "stored": stored, "unchanged": unchanged, "result": "ok"})
	a.logf("node: core-host: stored %d host secrets in %s from %s (move %s)", len(stored), place, p.From, p.Move)
	return nodeproto.CoreSecretsReceived{Move: p.Move, Stored: orEmpty(stored), Unchanged: orEmpty(unchanged)}, nil
}

// coreForget is core.secrets.forget: it removes the secrets this machine
// received over the node channel and did not hold before, never others,
// so that it cannot empty the store of a machine that runs the core.
func (a *Agent) coreForget() (any, *nodeproto.Error) {
	st, _, perr := a.coreStore()
	if perr != nil {
		return nil, perr
	}
	d, ok := st.(hostsecrets.Deleter)
	if !ok {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "this store cannot remove a secret")
	}
	var gone, failed []string
	for _, n := range a.coreReceived() {
		if err := d.Delete(n); err != nil {
			failed = append(failed, n)
			continue
		}
		gone = append(gone, n)
	}
	if err := a.writeReceived(failed); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	res := "ok"
	if len(failed) > 0 {
		res = "could not remove " + strings.Join(failed, ", ")
	}
	a.transferLog(map[string]any{"direction": "forgot", "names": gone, "result": res})
	if len(failed) > 0 {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "removed %s; could not remove %s", strings.Join(gone, ", "), strings.Join(failed, ", "))
	}
	return nodeproto.CoreSecretsForgotten{Forgotten: orEmpty(gone)}, nil
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// coreReceived are the names core.secrets.forget would remove.
func (a *Agent) coreReceived() []string {
	if a.Dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(a.Dir, coreReceivedFile))
	if err != nil {
		return nil
	}
	var f struct {
		Names []string `json:"names"`
	}
	if json.Unmarshal(data, &f) != nil {
		return nil
	}
	return slices.DeleteFunc(f.Names, func(n string) bool { return !slices.Contains(secretxfer.MoveNames, n) })
}

func (a *Agent) noteReceived(names []string) error {
	if len(names) == 0 {
		return nil
	}
	all := append(a.coreReceived(), names...)
	sort.Strings(all)
	return a.writeReceived(slices.Compact(all))
}

func (a *Agent) writeReceived(names []string) error {
	if a.Dir == "" {
		return nil
	}
	p := filepath.Join(a.Dir, coreReceivedFile)
	if len(names) == 0 {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, _ := json.Marshal(map[string][]string{"names": names})
	return writeFile(p, append(data, '\n'), 0o600)
}

// transferLog appends one line, names only, to the state dir's
// secrets-transfer.log.
func (a *Agent) transferLog(line map[string]any) {
	if a.Dir == "" {
		return
	}
	line["time"] = time.Now().UTC().Format(time.RFC3339)
	line["via"] = "node-channel"
	b, err := json.Marshal(line)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(a.Dir, TransferLogFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}
