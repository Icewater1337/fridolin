package main

import (
	"context"

	"github.com/Icewater1337/fridolin/internal/access"
	"github.com/Icewater1337/fridolin/internal/chat"
	mediafiles "github.com/Icewater1337/fridolin/internal/media"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// waSettings contains only the settings consumed by the transport. Each
// read corresponds to the same config read the adapter made before: one
// snapshot for authorization, later reads for media limits and chunk size.
type waSettings struct {
	access.Policy
	MediaDir      string
	MaxMediaBytes int64
	MaxChunkSize  int
}

type waDeviceContainer interface {
	GetFirstDevice(context.Context) (*wastore.Device, error)
	NewDevice() *wastore.Device
}

type waSentPoll struct {
	Chat     string
	Brain    string
	Question string
	Options  []string
}

type waPollStore interface {
	SentPoll(context.Context, string) (waSentPoll, error)
	SavePollVote(string, string, []string) error
}

type waVote struct {
	Poll     waSentPoll
	Voter    types.JID
	Selected []string
}

type waGroupRules struct{ Files, Forwards bool }

type waOptions struct {
	Settings   func() waSettings
	Container  waDeviceContainer
	Polls      waPollStore
	OnInbound  func(context.Context, chat.Inbound)
	OnIgnored  IgnoredHandler
	OnVote     func(waVote)
	OnChange   func(chat.Change)
	GroupRules func(string) waGroupRules
	Log        waLog.Logger
	Chunks     func(string, int) []string
	Outbound   waOutbound
	Text       waText
	Inbox      func(string) mediafiles.Inbox
}

// waOutbound preserves the shared recipient errors and partial-send contract
// while their implementation remains with the command's outbound path.
type waOutbound struct {
	Recipient func(string) (types.JID, error)
	Classify  func(error) error
	Chunks    func(context.Context, []string, func(string) error) error
}

// waText supplies the message catalogue and privacy formatting. The transport
// decides when a notice or log is emitted; formatting stays at composition.
type waText struct {
	ViewOnce, MediaFailed string
	MediaTooLarge         func(int64) string
	MaskSender            func(string, bool, string) string
	MaskJID               func(string, string) string
	Redact                func(string) string
}
