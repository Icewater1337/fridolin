// Package nodes is the core's side of the fleet (docs/NODES.md): the
// registry of nodes in nodes.json, the join codes, the two approval
// stages, and the WebSocket port node agents connect to, with the calls
// the core makes on them.
package nodes

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// RegistryFile is the registry's file name in the nodes folder.
const RegistryFile = "nodes.json"

// JoinCodeTTL is how long a join code stays valid.
const JoinCodeTTL = 10 * time.Minute

// Node is one node as nodes.json keeps it.
type Node struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Version   string `json:"version"`
	PublicKey []byte `json:"public_key"`
	// Tags are the owner's, editable here, in the dashboard and with
	// `fridolin nodes tags`; a new node starts with its OS's.
	Tags []string `json:"tags"`
	// Device is nodeproto.DevicePending, DeviceApproved or DeviceRevoked.
	Device string `json:"device"`
	// Declared is what the node said it offers when it last connected,
	// Approved what the owner approved (`fridolin nodes approve --commands`).
	Declared nodeproto.Capabilities `json:"declared"`
	Approved nodeproto.Capabilities `json:"approved"`
	JoinedAt time.Time              `json:"joined_at"`
	// DeviceApprovedAt and CommandsApprovedAt are when the stages were
	// approved; RevokedAt when it was revoked.
	DeviceApprovedAt   *time.Time `json:"device_approved_at,omitempty"`
	CommandsApprovedAt *time.Time `json:"commands_approved_at,omitempty"`
	RevokedAt          *time.Time `json:"revoked_at,omitempty"`
	LastSeen           *time.Time `json:"last_seen,omitempty"`
	// Address is where the node last connected from.
	Address   string               `json:"address,omitempty"`
	Resources *nodeproto.HostStats `json:"resources,omitempty"`
}

// Commands is the state of the node's second approval stage; a revoked
// node has none.
func (n *Node) Commands() string {
	switch {
	case n.Declared.Empty() || n.Device == nodeproto.DeviceRevoked:
		return nodeproto.CommandsNone
	case n.CommandsApprovedAt != nil && nodeproto.Covers(n.Approved, n.Declared):
		return nodeproto.CommandsApproved
	default:
		return nodeproto.CommandsPending
	}
}

// Effective is what the node may serve now: nothing until the device is
// approved, then the part of its declaration the owner approved.
func (n *Node) Effective() nodeproto.Capabilities {
	if n.Device != nodeproto.DeviceApproved {
		return nodeproto.Capabilities{}
	}
	return nodeproto.Intersect(n.Declared, n.Approved)
}

// State is what the core tells the node.
func (n *Node) State() nodeproto.State {
	st := nodeproto.State{Device: n.Device, Commands: n.Commands(), Approved: n.Effective()}
	switch {
	case n.Device == nodeproto.DevicePending:
		st.Message = "waiting for the owner to approve this device: fridolin nodes approve " + n.ID
	case n.Device == nodeproto.DeviceRevoked:
		st.Message = "this node was revoked"
	case st.Commands == nodeproto.CommandsPending:
		st.Message = "waiting for the owner to approve what this node offers: fridolin nodes approve " + n.ID + " --commands"
	}
	return st
}

type joinCode struct {
	// Hash is the SHA-256 of the code, hex: the file never holds a code.
	Hash      string    `json:"hash"`
	ExpiresAt time.Time `json:"expires_at"`
}

type registryFile struct {
	Version   int        `json:"version"`
	Nodes     []*Node    `json:"nodes"`
	JoinCodes []joinCode `json:"join_codes"`
}

// Errors of registry operations.
var (
	ErrUnknownNode  = errors.New("no such node")
	ErrRevoked      = errors.New("the node is revoked")
	ErrNotApproved  = errors.New("the device is not approved yet: approve it first")
	ErrBadJoinCode  = errors.New("the join code is unknown, used or expired")
	ErrInvalidInput = errors.New("invalid")
)

// Registry is nodes.json. It reads the file again when it changed on disk,
// so tags edited there by hand while the core runs are kept.
type Registry struct {
	path string
	now  func() time.Time

	mu    sync.Mutex
	file  registryFile
	stamp fileStamp
	// written is each node's last_seen as the file holds it, so that Seen
	// writes once a minute however often heartbeats come.
	written map[string]time.Time
}

type fileStamp struct {
	mod  time.Time
	size int64
	ok   bool
}

// OpenRegistry opens the registry in dir; the file is made on the first
// change.
func OpenRegistry(dir string, now func() time.Time) (*Registry, error) {
	if now == nil {
		now = time.Now
	}
	r := &Registry{path: filepath.Join(dir, RegistryFile), now: now}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.reloadLocked(); err != nil {
		return nil, err
	}
	return r, nil
}

// Path is the registry file.
func (r *Registry) Path() string { return r.path }

func (r *Registry) reloadLocked() error {
	st, err := os.Stat(r.path)
	if errors.Is(err, os.ErrNotExist) {
		if r.stamp.ok {
			r.file = registryFile{}
			r.written = nil
		}
		r.stamp = fileStamp{}
		return nil
	}
	if err != nil {
		return err
	}
	stamp := fileStamp{mod: st.ModTime(), size: st.Size(), ok: true}
	if stamp == r.stamp {
		return nil
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		return err
	}
	var f registryFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("reading %s: %w", r.path, err)
	}
	for _, n := range f.Nodes {
		if n == nil || n.ID == "" {
			return fmt.Errorf("reading %s: a node without an id", r.path)
		}
		n.Tags = normalizeTags(n.Tags)
	}
	f.Nodes = slices.DeleteFunc(f.Nodes, func(n *Node) bool { return n == nil })
	r.file, r.stamp = f, stamp
	r.noteWrittenLocked()
	return nil
}

// noteWrittenLocked records each node's last_seen as the file now holds
// it.
func (r *Registry) noteWrittenLocked() {
	r.written = make(map[string]time.Time, len(r.file.Nodes))
	for _, n := range r.file.Nodes {
		if n.LastSeen != nil {
			r.written[n.ID] = *n.LastSeen
		}
	}
}

// load reads the file again if it changed; an unreadable file keeps what
// was read before, and the error is returned for the caller to log.
func (r *Registry) load() error { return r.reloadLocked() }

func (r *Registry) saveLocked() error {
	r.file.Version = 1
	if r.file.Nodes == nil {
		r.file.Nodes = []*Node{}
	}
	if r.file.JoinCodes == nil {
		r.file.JoinCodes = []joinCode{}
	}
	data, err := json.MarshalIndent(r.file, "", "  ")
	if err != nil {
		return err
	}
	if err := fsx.WriteFileAtomic(r.path, append(data, '\n'), 0o600, false); err != nil {
		return err
	}
	r.noteWrittenLocked()
	if st, err := os.Stat(r.path); err == nil {
		r.stamp = fileStamp{mod: st.ModTime(), size: st.Size(), ok: true}
	}
	return nil
}

// update runs fn on the registry as on disk now and saves it when fn
// returns nil.
func (r *Registry) update(fn func(f *registryFile) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.load(); err != nil {
		return err
	}
	if err := fn(&r.file); err != nil {
		return err
	}
	return r.saveLocked()
}

func (f *registryFile) find(id string) *Node {
	i := slices.IndexFunc(f.Nodes, func(n *Node) bool { return n.ID == id })
	if i < 0 {
		return nil
	}
	return f.Nodes[i]
}

// clone deep-copies n through JSON, so callers never share the registry's.
func clone(n *Node) *Node {
	data, _ := json.Marshal(n)
	var c Node
	_ = json.Unmarshal(data, &c)
	return &c
}

// List returns copies of every node, by name and then ID.
func (r *Registry) List() ([]*Node, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.load()
	out := make([]*Node, 0, len(r.file.Nodes))
	for _, n := range r.file.Nodes {
		out = append(out, clone(n))
	}
	slices.SortFunc(out, func(a, b *Node) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, err
}

// Get returns a copy of the node with id.
func (r *Registry) Get(id string) (*Node, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.load()
	n := r.file.find(id)
	if n == nil {
		return nil, fmt.Errorf("%w %q", ErrUnknownNode, id)
	}
	return clone(n), nil
}

// codeAlphabet spells join codes: lower case, no padding.
var codeAlphabet = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func hashCode(code string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(code)))
	return hex.EncodeToString(sum[:])
}

// NewJoinCode makes a single-use join code, valid for JoinCodeTTL, and
// keeps only its hash.
func (r *Registry) NewJoinCode() (code string, expires time.Time, err error) {
	raw := make([]byte, 15)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	code = "fj-" + codeAlphabet.EncodeToString(raw)
	expires = r.now().Add(JoinCodeTTL).UTC().Truncate(time.Second)
	err = r.update(func(f *registryFile) error {
		f.pruneCodes(r.now())
		f.JoinCodes = append(f.JoinCodes, joinCode{Hash: hashCode(code), ExpiresAt: expires})
		return nil
	})
	return code, expires, err
}

func (f *registryFile) pruneCodes(now time.Time) {
	f.JoinCodes = slices.DeleteFunc(f.JoinCodes, func(c joinCode) bool { return !now.Before(c.ExpiresAt) })
}

// Hello records the node of a verified hello. A device the registry does
// not know joins with code, which it uses up, and starts pending; a
// revoked one is refused with ErrRevoked. It returns the node as now
// recorded.
func (r *Registry) Hello(h nodeproto.Hello, address string) (*Node, error) {
	var out *Node
	err := r.update(func(f *registryFile) error {
		now := r.now().UTC().Truncate(time.Second)
		n := f.find(h.DeviceID)
		if n == nil {
			if h.JoinCode == "" {
				return ErrBadJoinCode
			}
			f.pruneCodes(r.now())
			want := hashCode(h.JoinCode)
			i := slices.IndexFunc(f.JoinCodes, func(c joinCode) bool { return c.Hash == want })
			if i < 0 {
				return ErrBadJoinCode
			}
			f.JoinCodes = slices.Delete(f.JoinCodes, i, i+1)
			n = &Node{ID: h.DeviceID, PublicKey: h.PublicKey, Device: nodeproto.DevicePending, JoinedAt: now, Tags: osTags(h.OS)}
			f.Nodes = append(f.Nodes, n)
		}
		if n.Device == nodeproto.DeviceRevoked {
			return ErrRevoked
		}
		if string(n.PublicKey) != string(h.PublicKey) {
			return fmt.Errorf("%w: another key for node %s", ErrInvalidInput, n.ID)
		}
		n.Name, n.OS, n.Arch, n.Version = h.Name, h.OS, h.Arch, h.Version
		n.Declared = h.Capabilities
		n.LastSeen, n.Address = &now, address
		out = clone(n)
		return nil
	})
	return out, err
}

// Declare records what a connected node offers now. Offering more than was
// approved makes its commands pending again, as at a new connection.
func (r *Registry) Declare(id string, caps nodeproto.Capabilities) (*Node, error) {
	return r.change(id, func(n *Node, _ time.Time) error {
		if n.Device == nodeproto.DeviceRevoked {
			return ErrRevoked
		}
		n.Declared = caps
		return nil
	})
}

// SeenWriteEvery is how often Seen writes a connected node's last_seen
// and resources to the file: other processes (fridolin doctor, a restarted
// bridge) read them there.
const SeenWriteEvery = time.Minute

// Seen records a heartbeat: when, and the host's resources. It writes the
// file when what the file holds for the node is SeenWriteEvery old, or
// persist says to; in between it keeps them in memory.
func (r *Registry) Seen(id string, stats nodeproto.HostStats, persist bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.load()
	n := r.file.find(id)
	if n == nil {
		return fmt.Errorf("%w %q", ErrUnknownNode, id)
	}
	now := r.now().UTC().Truncate(time.Second)
	n.LastSeen, n.Resources = &now, &stats
	if w, ok := r.written[id]; persist || !ok || now.Sub(w) >= SeenWriteEvery {
		return r.saveLocked()
	}
	// Kept in memory until the next write: the file's stamp stays, so the
	// next load does not throw it away.
	return nil
}

// Flush writes what Seen kept in memory for node id, when the file does
// not hold it yet: when the node disconnects, so the file says when it
// was last heard from.
func (r *Registry) Flush(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.load()
	n := r.file.find(id)
	if n == nil || n.LastSeen == nil {
		return nil
	}
	if w, ok := r.written[id]; ok && w.Equal(*n.LastSeen) {
		return nil
	}
	return r.saveLocked()
}

// ApproveDevice approves the first stage.
func (r *Registry) ApproveDevice(id string) (*Node, error) {
	return r.change(id, func(n *Node, now time.Time) error {
		switch n.Device {
		case nodeproto.DeviceRevoked:
			return ErrRevoked
		case nodeproto.DevicePending:
			n.Device, n.DeviceApprovedAt = nodeproto.DeviceApproved, &now
		}
		return nil
	})
}

// ApproveCommands approves what the node declares now, the second stage.
func (r *Registry) ApproveCommands(id string) (*Node, error) {
	return r.change(id, func(n *Node, now time.Time) error {
		switch n.Device {
		case nodeproto.DeviceRevoked:
			return ErrRevoked
		case nodeproto.DevicePending:
			return ErrNotApproved
		}
		// The backup folders are the owner's picks, not part of what the
		// node declares: approving again keeps them.
		var folders []string
		if n.Approved.Backup != nil {
			folders = n.Approved.Backup.Folders
		}
		n.Approved, n.CommandsApprovedAt = n.Declared, &now
		if b := n.Declared.Backup; b != nil {
			n.Approved.Backup = &nodeproto.BackupCap{Roots: slices.Clone(b.Roots), Folders: folders}
		}
		return nil
	})
}

// Revoke revokes the node: it keeps its record, so that it is refused
// when it connects again, and loses every approval.
func (r *Registry) Revoke(id string) (*Node, error) {
	return r.change(id, func(n *Node, now time.Time) error {
		if n.Device != nodeproto.DeviceRevoked {
			n.Device, n.RevokedAt = nodeproto.DeviceRevoked, &now
		}
		n.Approved, n.CommandsApprovedAt = nodeproto.Capabilities{}, nil
		return nil
	})
}

// tagRE is one tag: lower case, digits and ._:-, as `memory:16gb`.
var tagRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,31}$`)

// MaxTags bounds a node's tags.
const MaxTags = 32

func normalizeTags(tags []string) []string {
	out := []string{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out
}

// CheckTags reports what is wrong with tags.
func CheckTags(tags []string) error {
	if len(tags) > MaxTags {
		return fmt.Errorf("%w: at most %d tags", ErrInvalidInput, MaxTags)
	}
	for _, t := range normalizeTags(tags) {
		if !tagRE.MatchString(t) {
			return fmt.Errorf("%w tag %q: lower-case letters, digits and . _ : -, up to 32", ErrInvalidInput, t)
		}
	}
	return nil
}

// SetTags replaces the node's tags.
func (r *Registry) SetTags(id string, tags []string) (*Node, error) {
	if err := CheckTags(tags); err != nil {
		return nil, err
	}
	return r.change(id, func(n *Node, _ time.Time) error {
		n.Tags = normalizeTags(tags)
		return nil
	})
}

func (r *Registry) change(id string, fn func(*Node, time.Time) error) (*Node, error) {
	var out *Node
	err := r.update(func(f *registryFile) error {
		n := f.find(id)
		if n == nil {
			return fmt.Errorf("%w %q", ErrUnknownNode, id)
		}
		if err := fn(n, r.now().UTC().Truncate(time.Second)); err != nil {
			return err
		}
		out = clone(n)
		return nil
	})
	return out, err
}

// osTags are the tags a new node starts with.
func osTags(goos string) []string {
	switch goos {
	case "darwin":
		return []string{"macos"}
	case "windows", "linux":
		return []string{goos}
	}
	return []string{}
}
