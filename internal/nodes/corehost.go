package nodes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// CoreTransferOptions are a host-secrets transfer's (#187 §2b): Run moves
// them (without it, a dry run only reports), Replace lets a value
// replace a different one already on the target.
type CoreTransferOptions struct {
	Run, Replace bool
	// Move is the move id the transfer is for, "" for a new one.
	Move string
}

// CoreTransferEnd is one side of a transfer as the report shows it.
type CoreTransferEnd struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	// Stored and Missing are the host secrets a move carries that the
	// machine holds, and lacks; SecretsError says why it could not tell.
	Stored       []string `json:"stored"`
	Missing      []string `json:"missing"`
	SecretsError string   `json:"secrets_error,omitempty"`
}

// CoreTransferReport is what a transfer found and did. The core sees
// names only: the secrets go encrypted from the source's agent to the
// target's.
type CoreTransferReport struct {
	Source CoreTransferEnd `json:"source"`
	Target CoreTransferEnd `json:"target"`
	// Pinned says whether the source pinned the target's device key;
	// Confirmed whether the owner confirmed the target as a core host,
	// so that the core names the source its key. Without either nothing
	// can go: NeedsConfirm is then set, and only the owner's vote on a
	// move (MoveOptions.ConfirmTarget) clears it.
	Pinned       bool `json:"pinned"`
	Confirmed    bool `json:"confirmed"`
	NeedsConfirm bool `json:"needs_confirm,omitempty"`
	// Problems keep the transfer from running.
	Problems []string `json:"problems"`
	Ran      bool     `json:"ran"`
	Move     string   `json:"move,omitempty"`
	// Sent are the names in the bundle; Stored and Unchanged what the
	// target did with them.
	Sent      []string `json:"sent,omitempty"`
	Stored    []string `json:"stored,omitempty"`
	Unchanged []string `json:"unchanged,omitempty"`
	// FilesSent are the secret files of the core's credentials folder
	// that went along (#187), FilesStored and FilesKept what the target
	// wrote and what it had already; FilesError why the source sent none.
	FilesSent   []string `json:"files_sent,omitempty"`
	FilesStored []string `json:"files_stored,omitempty"`
	FilesKept   []string `json:"files_kept,omitempty"`
	FilesError  string   `json:"files_error,omitempty"`
}

// ErrCoreTransfer is a transfer whose checks failed.
var ErrCoreTransfer = errors.New("the host secrets cannot be transferred")

// CoreSecretsTransfer moves the host secrets from node src to node dst
// over the node channel: dst makes a one-time key and signs it with its
// device key, src checks the signature against the key it pinned for
// dst, or the one the core names for a confirmed core host, and
// encrypts its secrets to it, dst decrypts and stores them. The core
// relays the offer and the ciphertext. Without o.Run it only checks
// both sides (core.check) and reports. It never confirms a target: a
// machine the owner has not confirmed needs a move from WhatsApp.
func (s *Service) CoreSecretsTransfer(ctx context.Context, src, dst string, o CoreTransferOptions) (CoreTransferReport, error) {
	rep, _, err := s.coreTransferCheck(ctx, src, dst)
	if err != nil {
		return rep, err
	}
	if rep.NeedsConfirm {
		rep.Problems = append(rep.Problems, unconfirmedProblem(rep.Target.Name))
	}
	return s.coreTransferRun(ctx, src, dst, rep, o)
}

// unconfirmedProblem is what keeps the host secrets from a machine the
// owner has not confirmed as a core host, outside their vote.
func unconfirmedProblem(name string) string {
	return fmt.Sprintf("%s has never been confirmed as a core host: only your vote in WhatsApp lets it hold the secrets (!movecore %s)", name, name)
}

// coreTransferCheck checks both sides of a transfer (core.check) and
// returns what each answered.
func (s *Service) coreTransferCheck(ctx context.Context, src, dst string) (CoreTransferReport, [2]nodeproto.CoreCheckResult, error) {
	var rep CoreTransferReport
	checks := [2]nodeproto.CoreCheckResult{}
	rep.Problems = []string{}
	if src == dst {
		return rep, checks, fmt.Errorf("%w: the source and the target are the same node", ErrInvalidInput)
	}
	ends := [2]*CoreTransferEnd{&rep.Source, &rep.Target}
	for i, id := range []string{src, dst} {
		n, err := s.reg.Get(id)
		if err != nil {
			return rep, checks, err
		}
		*ends[i] = CoreTransferEnd{ID: n.ID, Name: n.Name, Fingerprint: nodeproto.Fingerprint(n.PublicKey), Stored: []string{}, Missing: []string{}}
		switch {
		case n.Device != nodeproto.DeviceApproved:
			rep.Problems = append(rep.Problems, fmt.Sprintf("%s (%s): the device is %s", n.Name, n.ID, n.Device))
			continue
		case n.Effective().CoreHost == nil:
			rep.Problems = append(rep.Problems, fmt.Sprintf("%s (%s) is no core host: its core-host is off, or its fridolin is too old to offer it by default (on that machine, fridolin node core-host on, or fridolin nodes update %s)", n.Name, n.ID, n.ID))
			continue
		}
		if i == 1 {
			rep.Confirmed = n.CoreHostConfirmed()
		}
		if err := s.Call(ctx, id, nodeproto.MethodCoreCheck, nil, &checks[i]); err != nil {
			rep.Problems = append(rep.Problems, fmt.Sprintf("%s (%s): core.check: %v", n.Name, n.ID, err))
			continue
		}
		c := checks[i]
		ends[i].SecretsError = c.SecretsError
		for _, sp := range c.Secrets {
			if sp.Stored {
				ends[i].Stored = append(ends[i].Stored, sp.Name)
			} else {
				ends[i].Missing = append(ends[i].Missing, sp.Name)
			}
		}
		if c.SecretsError != "" {
			rep.Problems = append(rep.Problems, fmt.Sprintf("%s (%s): its secret store: %s", n.Name, n.ID, c.SecretsError))
		}
	}
	rep.Pinned = slices.ContainsFunc(checks[0].SendTo, func(p nodeproto.CoreHostPinView) bool {
		return p.ID == dst && p.Fingerprint == rep.Target.Fingerprint
	})
	if checks[0].ID != "" && !rep.Pinned {
		switch {
		case !checks[0].TrustsCore:
			rep.Problems = append(rep.Problems, fmt.Sprintf("%s runs a fridolin that sends host secrets to pinned targets only: update it first (fridolin nodes update %s)", rep.Source.Name, src))
		case !rep.Confirmed:
			rep.NeedsConfirm = true
		}
	}
	if checks[0].ID != "" && len(rep.Source.Stored) == 0 && rep.Source.SecretsError == "" {
		rep.Problems = append(rep.Problems, rep.Source.Name+" holds none of the host secrets")
	}
	return rep, checks, nil
}

// coreTransferRun moves the secrets once coreTransferCheck found nothing
// against it (without o.Run it returns the report as it is).
func (s *Service) coreTransferRun(ctx context.Context, src, dst string, rep CoreTransferReport, o CoreTransferOptions) (CoreTransferReport, error) {
	if !o.Run {
		return rep, nil
	}
	if len(rep.Problems) > 0 {
		return rep, fmt.Errorf("%w: %s", ErrCoreTransfer, rep.Problems[0])
	}
	params := nodeproto.CoreSecretsSendParams{}
	if !rep.Pinned {
		// The key the core approved at the target's join, named only for
		// a core host the owner confirmed, as it is now.
		n, err := s.reg.Get(dst)
		if err != nil {
			return rep, err
		}
		if !n.CoreHostConfirmed() {
			return rep, fmt.Errorf("%w: %s", ErrCoreTransfer, unconfirmedProblem(n.Name))
		}
		params.TargetKey = hex.EncodeToString(n.PublicKey)
	}
	rep.Move = o.Move
	if rep.Move == "" {
		rep.Move = newMoveID()
	}
	var offer nodeproto.CoreSecretsOffer
	if err := s.Call(ctx, dst, nodeproto.MethodCoreSecretsOffer, nodeproto.CoreSecretsOfferParams{Move: rep.Move}, &offer); err != nil {
		return rep, fmt.Errorf("%s: the offer: %w", rep.Target.Name, err)
	}
	params.Offer = offer
	var bundle nodeproto.CoreSecretsBundle
	if err := s.Call(ctx, src, nodeproto.MethodCoreSecretsSend, params, &bundle); err != nil {
		return rep, fmt.Errorf("%s: sending: %w", rep.Source.Name, err)
	}
	rep.Sent, rep.FilesSent, rep.FilesError = bundle.Names, bundle.Files, bundle.FilesError
	var got nodeproto.CoreSecretsReceived
	if err := s.Call(ctx, dst, nodeproto.MethodCoreSecretsReceive, nodeproto.CoreSecretsReceiveParams{Move: rep.Move, From: src, Ciphertext: bundle.Ciphertext, Replace: o.Replace}, &got); err != nil {
		return rep, fmt.Errorf("%s: storing: %w", rep.Target.Name, err)
	}
	rep.Ran, rep.Stored, rep.Unchanged, rep.FilesStored, rep.FilesKept = true, got.Stored, got.Unchanged, got.FilesStored, got.FilesKept
	s.logf("nodes: moved %d host secrets and %d secret files from %s to %s (move %s; %d stored, %d unchanged, %d files stored, %d kept)", len(bundle.Names), len(bundle.Files), src, dst, rep.Move, len(got.Stored), len(got.Unchanged), len(got.FilesStored), len(got.FilesKept))
	return rep, nil
}

// newMoveID is a fresh move id.
func newMoveID() string {
	rnd := make([]byte, 2)
	_, _ = rand.Read(rnd)
	return nodeproto.NewMoveID(time.Now(), hex.EncodeToString(rnd))
}

// CoreHostView is how the fleet shows a core host: whether the owner
// confirmed it (their vote on a move to it), the device key's
// fingerprint, which `fridolin node core-host show` prints there, the
// targets it pins, and what moving the core there means
// (nodeproto.CoreHostApproval).
type CoreHostView struct {
	Confirmed   bool     `json:"confirmed"`
	Fingerprint string   `json:"fingerprint"`
	SendTo      []string `json:"send_to"`
	Approval    string   `json:"approval"`
}

// coreHostView is n's CoreHostView, nil for a node that does not offer
// core-host.
func coreHostView(n *Node) *CoreHostView {
	ch := n.Declared.CoreHost
	if ch == nil || n.Device == nodeproto.DeviceRevoked {
		return nil
	}
	return &CoreHostView{Confirmed: n.CoreHostConfirmed(), Fingerprint: nodeproto.Fingerprint(n.PublicKey), SendTo: append([]string{}, ch.SendTo...), Approval: nodeproto.CoreHostApproval}
}
