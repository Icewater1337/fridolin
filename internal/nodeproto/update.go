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
	"unicode/utf8"
)

// UpdateCap (update) lets the core replace the node's fridolin with the
// build of its own version (docs/NODES.md, "Updates"). Every agent that
// knows it declares it; the owner approves it once, like the rest, and a
// node whose update is not approved never replaces its binary. The core
// streams the binary in chunks and signs what it is with its update key
// (UpdateMessage), which the node checks against the key it pinned:
// at join, or, for a node that joined before it knew the key, at its
// first update, which needs the approval first.
//
// A node in a box (Box) is updated by image instead: the core signs the
// digests of its own version's images (BoxUpdateMessage), the node keeps
// the request in its volume, and the box's updater on the host checks,
// pulls and recreates (docs/NODES.md, "Box updates").
type UpdateCap struct {
	Box bool `json:"box,omitempty"`
}

// The update methods. Each needs the update capability.
const (
	// MethodUpdateWrite appends a chunk of the new binary, gzip'd, to a
	// transfer, making it at offset 0 (FrideChunkParams → FrideTransfer).
	MethodUpdateWrite = "update.write"
	// MethodUpdateApply checks a transfer against the core's signature,
	// puts it in place of the running binary, keeping the old one as
	// .prev, and restarts the agent (UpdateApplyParams → UpdateApplyResult).
	MethodUpdateApply = "update.apply"
	// MethodUpdateDrop removes a transfer (FrideChunkParams with the ID →
	// nothing).
	MethodUpdateDrop = "update.drop"
	// MethodUpdateBox asks a node in a box for the images of a version
	// (BoxUpdateParams → BoxUpdateResult).
	MethodUpdateBox = "update.box"
)

// UpdateMethods lists the update methods.
var UpdateMethods = []string{MethodUpdateWrite, MethodUpdateApply, MethodUpdateDrop, MethodUpdateBox}

// BoxUpdateParams asks a box for the images of Version, pinned by digest:
// NodeImage for its node service, FrideImage for its fride service, as
// the core's Signature over BoxUpdateMessage says.
type BoxUpdateParams struct {
	Version    string `json:"version"`
	NodeImage  string `json:"node_image"`
	FrideImage string `json:"fride_image"`
	Signature  []byte `json:"signature"`
}

// imageDigestRE is an image named by its digest: a registry path, then
// @sha256: and 64 hex digits.
var imageDigestRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(?:\.[a-z0-9._-]+)*(?::[0-9]+)?(?:/[a-z0-9][a-z0-9._-]*)+@sha256:[0-9a-f]{64}$`)

// CheckImageDigest reports what is wrong with an image reference that must
// name a digest.
func CheckImageDigest(ref string) error {
	if len(ref) > 512 || !imageDigestRE.MatchString(ref) {
		return fmt.Errorf("image %q is not a registry path pinned by digest (…@sha256:<64 hex digits>)", ref)
	}
	return nil
}

// Check reports what is wrong with the params.
func (p BoxUpdateParams) Check() error {
	if err := CheckVersion(p.Version); err != nil {
		return err
	}
	if err := CheckImageDigest(p.NodeImage); err != nil {
		return err
	}
	if err := CheckImageDigest(p.FrideImage); err != nil {
		return err
	}
	if len(p.Signature) != ed25519.SignatureSize {
		return errors.New("the signature is missing")
	}
	return nil
}

// BoxUpdateMessage is what the core signs for a box update: the protocol,
// the version and both images by digest.
func BoxUpdateMessage(version, nodeImage, frideImage string) []byte {
	return []byte("fridolin-box-update/v1\x00" + version + "\x00" + nodeImage + "\x00" + frideImage)
}

// SignBoxUpdate signs a box update with the core's update key.
func SignBoxUpdate(key ed25519.PrivateKey, version, nodeImage, frideImage string) []byte {
	return ed25519.Sign(key, BoxUpdateMessage(version, nodeImage, frideImage))
}

// VerifyBoxUpdate checks the signature of p with the core's public key.
func VerifyBoxUpdate(pub []byte, p BoxUpdateParams) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), BoxUpdateMessage(p.Version, p.NodeImage, p.FrideImage), p.Signature)
}

// BoxUpdateResult is a box's answer: the request is kept for the box's
// updater, which picks it up within UpdaterMinutes.
type BoxUpdateResult struct {
	From           string `json:"from"`
	To             string `json:"to"`
	UpdaterMinutes int    `json:"updater_minutes"`
}

// BoxUpdaterMinutes is how often a box's updater looks for a request.
const BoxUpdaterMinutes = 60

// Bounds of an update.
const (
	// MaxUpdateTransferBytes bounds the gzip'd binary a node stages.
	MaxUpdateTransferBytes = 256 << 20
	// MaxBinaryBytes bounds the binary unpacked.
	MaxBinaryBytes = 512 << 20
	// UpdateTrialMinutes is how long a node's new binary has to connect to
	// the core before the node puts the previous one back.
	UpdateTrialMinutes = 5
)

// UpdateApplyParams applies a staged transfer: the gzip'd binary of
// Version for OS and Arch, whose unpacked bytes have SHA256 (hex) and
// Size, as the core's Signature over UpdateMessage says.
type UpdateApplyParams struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	// TransferSHA256 is the SHA-256 of the gzip'd bytes as the core sent
	// them: a chunk that changed on the way is refused before unpacking.
	TransferSHA256 string `json:"transfer_sha256"`
	Signature      []byte `json:"signature"`
}

var (
	sha256HexRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	platformRE  = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
)

// Check reports what is wrong with the params.
func (p UpdateApplyParams) Check() error {
	switch {
	case CheckVersion(p.Version) != nil:
		return CheckVersion(p.Version)
	case !platformRE.MatchString(p.OS) || !platformRE.MatchString(p.Arch):
		return fmt.Errorf("os %q and arch %q", p.OS, p.Arch)
	case !sha256HexRE.MatchString(p.SHA256) || !sha256HexRE.MatchString(p.TransferSHA256):
		return errors.New("sha256 and transfer_sha256 are 64 hex digits")
	case p.Size <= 0 || p.Size > MaxBinaryBytes:
		return fmt.Errorf("size from 1 to %d bytes", MaxBinaryBytes)
	case len(p.Signature) != ed25519.SignatureSize:
		return errors.New("the signature is missing")
	}
	return nil
}

// UpdateApplyResult is an update in place: the agent restarts into To
// right after it answered.
type UpdateApplyResult struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Executable is the binary that was replaced; the old one is beside
	// it with .prev appended.
	Executable string `json:"executable"`
	// TrialMinutes is how long the new binary has to connect before the
	// node puts the old one back.
	TrialMinutes int `json:"trial_minutes"`
}

// CheckVersion reports whether v is a version a node may report and an
// update may name: printable, no spaces, at most 64 bytes.
func CheckVersion(v string) error {
	if v == "" || len(v) > 64 || !utf8.ValidString(v) || strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return fmt.Errorf("version %q: 1 to 64 printable characters without spaces", v)
	}
	return nil
}

// UpdateMessage is what the core signs for an update: the protocol, and
// the version, OS, architecture and unpacked SHA-256 of the binary, so a
// signature vouches for one build of one platform only.
func UpdateMessage(version, goos, goarch, sha256Hex string) []byte {
	return []byte("fridolin-node-update/v1\x00" + version + "\x00" + goos + "\x00" + goarch + "\x00" + sha256Hex)
}

// SignUpdate signs an update with the core's update key.
func SignUpdate(key ed25519.PrivateKey, version, goos, goarch, sha256Hex string) []byte {
	return ed25519.Sign(key, UpdateMessage(version, goos, goarch, sha256Hex))
}

// VerifyUpdate checks the signature of p with the core's public key.
func VerifyUpdate(pub []byte, p UpdateApplyParams) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), UpdateMessage(p.Version, p.OS, p.Arch, p.SHA256), p.Signature)
}

// KeyFingerprint names a public key for people: the first 16 hex digits
// of its SHA-256.
func KeyFingerprint(pub []byte) string {
	if len(pub) == 0 {
		return ""
	}
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// EventUpdate is a node's report of how an update went (an UpdateReport),
// sent after it connected again.
const EventUpdate = "update"

// UpdateReport is how an update ended on the node.
type UpdateReport struct {
	From string `json:"from"`
	To   string `json:"to"`
	OK   bool   `json:"ok"`
	// Error says why the node went back to From.
	Error string `json:"error,omitempty"`
}

// versionRE reads the versions builds are stamped with: a git describe
// of a tag (v1.2.3, v1.2.3-4-gabc1234, with -dirty), or a bare v1.2.3.
var versionRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-(\d+)-g[0-9a-f]+)?(?:-dirty)?$`)

// parseVersion reads v into its numbers: major, minor, patch, commits
// since the tag.
func parseVersion(v string) ([4]int, bool) {
	m := versionRE.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return [4]int{}, false
	}
	var out [4]int
	for i := range 4 {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [4]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// commitRE reads a version named by its commit: `git describe --always`
// of a checkout without tags (0284f03e, with -dirty) or a build without a
// stamp (dev-0284f03, with +dirty).
var commitRE = regexp.MustCompile(`^(?:dev-)?([0-9a-f]{7,40})([-+]dirty)?$`)

// sameCommit reports whether a and b name the same commit with hashes of
// different lengths: git abbreviates as long as a repository needs, so two
// clones (or a stamped and an unstamped build) can spell one commit
// 0284f03 and 0284f03e. A dirty build is never the same as another.
func sameCommit(a, b string) bool {
	ma, mb := commitRE.FindStringSubmatch(strings.TrimSpace(a)), commitRE.FindStringSubmatch(strings.TrimSpace(b))
	if ma == nil || mb == nil || ma[2] != "" || mb[2] != "" {
		return false
	}
	return strings.HasPrefix(ma[1], mb[1]) || strings.HasPrefix(mb[1], ma[1])
}

// CompareVersions orders two versions: -1 when a is older than b, 1 when
// newer, 0 when they are the same. ok is false when the two cannot be
// ordered (a commit hash, dev): then only equal or not is known, and cmp
// is 0 for equal and -1 for different, so that a different build counts
// as one to update.
func CompareVersions(a, b string) (cmp int, ok bool) {
	if a == b || sameCommit(a, b) {
		return 0, true
	}
	va, oka := parseVersion(a)
	vb, okb := parseVersion(b)
	if !oka || !okb {
		return -1, false
	}
	for i := range 4 {
		switch {
		case va[i] < vb[i]:
			return -1, true
		case va[i] > vb[i]:
			return 1, true
		}
	}
	// Same numbers, other spelling (a -dirty build): not the same build.
	return -1, false
}

// Outdated reports whether a node running node should be updated to
// core: it is older, or a build that cannot be ordered and differs.
func Outdated(node, core string) bool {
	cmp, _ := CompareVersions(node, core)
	return cmp < 0
}
