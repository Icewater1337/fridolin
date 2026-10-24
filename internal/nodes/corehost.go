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
	// Pinned says whether the source trusts the target's device key;
	// without it nothing can go.
	Pinned bool `json:"pinned"`
	// Problems keep the transfer from running.
	Problems []string `json:"problems"`
	Ran      bool     `json:"ran"`
	Move     string   `json:"move,omitempty"`
	// Sent are the names in the bundle; Stored and Unchanged what the
	// target did with them.
	Sent      []string `json:"sent,omitempty"`
	Stored    []string `json:"stored,omitempty"`
	Unchanged []string `json:"unchanged,omitempty"`
}

// ErrCoreTransfer is a transfer whose checks failed.
var ErrCoreTransfer = errors.New("the host secrets cannot be transferred")

// CoreSecretsTransfer moves the host secrets from node src to node dst
// over the node channel: dst makes a one-time key and signs it with its
// device key, src checks the signature against the key it pinned for
// dst and encrypts its secrets to it, dst decrypts and stores them. The
// core relays the offer and the ciphertext. Without o.Run it only checks
// both sides (core.check) and reports.
func (s *Service) CoreSecretsTransfer(ctx context.Context, src, dst string, o CoreTransferOptions) (CoreTransferReport, error) {
	var rep CoreTransferReport
	rep.Problems = []string{}
	if src == dst {
		return rep, fmt.Errorf("%w: the source and the target are the same node", ErrInvalidInput)
	}
	ends := [2]*CoreTransferEnd{&rep.Source, &rep.Target}
	checks := [2]nodeproto.CoreCheckResult{}
	for i, id := range []string{src, dst} {
		n, err := s.reg.Get(id)
		if err != nil {
			return rep, err
		}
		*ends[i] = CoreTransferEnd{ID: n.ID, Name: n.Name, Fingerprint: nodeproto.Fingerprint(n.PublicKey), Stored: []string{}, Missing: []string{}}
		switch {
		case n.Device != nodeproto.DeviceApproved:
			rep.Problems = append(rep.Problems, fmt.Sprintf("%s (%s): the device is %s", n.Name, n.ID, n.Device))
			continue
		case n.Effective().CoreHost == nil && n.Declared.CoreHost != nil:
			rep.Problems = append(rep.Problems, fmt.Sprintf("%s (%s): core-host waits for your approval (fridolin nodes approve %s --commands)", n.Name, n.ID, n.ID))
			continue
		case n.Effective().CoreHost == nil:
			rep.Problems = append(rep.Problems, fmt.Sprintf("%s (%s) is no core host: on that machine, fridolin node core-host on", n.Name, n.ID))
			continue
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
		rep.Problems = append(rep.Problems, fmt.Sprintf("%s does not trust %s: on %s, fridolin node core-host trust %s --key KEY, with the key fridolin node core-host show prints on %s (fingerprint %s)",
			rep.Source.Name, rep.Target.Name, rep.Source.Name, dst, rep.Target.Name, rep.Target.Fingerprint))
	}
	if checks[0].ID != "" && len(rep.Source.Stored) == 0 && rep.Source.SecretsError == "" {
		rep.Problems = append(rep.Problems, rep.Source.Name+" holds none of the host secrets")
	}
	if !o.Run {
		return rep, nil
	}
	if len(rep.Problems) > 0 {
		return rep, fmt.Errorf("%w: %s", ErrCoreTransfer, rep.Problems[0])
	}
	rnd := make([]byte, 2)
	_, _ = rand.Read(rnd)
	rep.Move = nodeproto.NewMoveID(time.Now(), hex.EncodeToString(rnd))
	var offer nodeproto.CoreSecretsOffer
	if err := s.Call(ctx, dst, nodeproto.MethodCoreSecretsOffer, nodeproto.CoreSecretsOfferParams{Move: rep.Move}, &offer); err != nil {
		return rep, fmt.Errorf("%s: the offer: %w", rep.Target.Name, err)
	}
	var bundle nodeproto.CoreSecretsBundle
	if err := s.Call(ctx, src, nodeproto.MethodCoreSecretsSend, nodeproto.CoreSecretsSendParams{Offer: offer}, &bundle); err != nil {
		return rep, fmt.Errorf("%s: sending: %w", rep.Source.Name, err)
	}
	rep.Sent = bundle.Names
	var got nodeproto.CoreSecretsReceived
	if err := s.Call(ctx, dst, nodeproto.MethodCoreSecretsReceive, nodeproto.CoreSecretsReceiveParams{Move: rep.Move, From: src, Ciphertext: bundle.Ciphertext, Replace: o.Replace}, &got); err != nil {
		return rep, fmt.Errorf("%s: storing: %w", rep.Target.Name, err)
	}
	rep.Ran, rep.Stored, rep.Unchanged = true, got.Stored, got.Unchanged
	s.logf("nodes: moved %d host secrets from %s to %s (move %s; %d stored, %d unchanged)", len(bundle.Names), src, dst, rep.Move, len(got.Stored), len(got.Unchanged))
	return rep, nil
}

// coreHostPending reports whether n offers a core-host its approval does
// not cover: a new one, or a changed core-host.json.
func coreHostPending(n *Node) bool {
	d := n.Declared.CoreHost
	return d != nil && n.Device != nodeproto.DeviceRevoked && (n.Approved.CoreHost == nil || n.Approved.CoreHost.Hash != d.Hash)
}

// noteCoreHost tells OnCoreHost once per core-host.json that node n asks
// for core-host, so the owner hears of every change of that file.
func (s *Service) noteCoreHost(n *Node) {
	if s.onCoreHost == nil || !coreHostPending(n) {
		return
	}
	s.mu.Lock()
	seen := s.coreHostSeen[n.ID] == n.Declared.CoreHost.Hash
	s.coreHostSeen[n.ID] = n.Declared.CoreHost.Hash
	s.mu.Unlock()
	if !seen {
		s.onCoreHost(clone(n))
	}
}

// CoreHostView is how the fleet shows a core host: whether its
// core-host.json waits for approval, the device key's fingerprint the
// owner compares with what `fridolin node core-host show` prints there,
// and what approving it means (nodeproto.CoreHostApproval).
type CoreHostView struct {
	Pending     bool     `json:"pending"`
	Fingerprint string   `json:"fingerprint"`
	SendTo      []string `json:"send_to"`
	Approval    string   `json:"approval"`
}

// coreHostView is n's CoreHostView, nil for a node that neither offers
// core-host nor had it approved.
func coreHostView(n *Node) *CoreHostView {
	ch := n.Declared.CoreHost
	if ch == nil {
		ch = n.Approved.CoreHost
	}
	if ch == nil {
		return nil
	}
	return &CoreHostView{Pending: coreHostPending(n), Fingerprint: nodeproto.Fingerprint(n.PublicKey), SendTo: append([]string{}, ch.SendTo...), Approval: nodeproto.CoreHostApproval}
}
