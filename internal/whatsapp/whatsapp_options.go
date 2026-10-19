package whatsapp

import (
	"context"

	"github.com/Icewater1337/fridolin/internal/access"
	"github.com/Icewater1337/fridolin/internal/chat"
	mediafiles "github.com/Icewater1337/fridolin/internal/media"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Settings contains only the settings consumed by the transport. Each
// read corresponds to the same config read the adapter made before: one
// snapshot for authorization, later reads for media limits and chunk size.
type Settings struct {
	access.Policy
	MediaDir      string
	MaxMediaBytes int64
	MaxChunkSize  int
}

type DeviceContainer interface {
	GetFirstDevice(context.Context) (*wastore.Device, error)
	NewDevice() *wastore.Device
}

type SentPoll struct {
	Chat     string
	Brain    string
	Question string
	Options  []string
}

type PollStore interface {
	SentPoll(context.Context, string) (SentPoll, error)
	SavePollVote(string, string, []string) error
}

type Vote struct {
	Poll     SentPoll
	Voter    types.JID
	Selected []string
}

type GroupRules struct{ Files, Forwards bool }

type Options struct {
	Settings   func() Settings
	Container  DeviceContainer
	Polls      PollStore
	OnInbound  func(context.Context, chat.Inbound)
	OnIgnored  IgnoredHandler
	OnVote     func(Vote)
	OnChange   func(chat.Change)
	GroupRules func(string) GroupRules
	Log        waLog.Logger
	Chunks     func(string, int) []string
	Identity   Identity
	Outbound   Outbound
	Text       Text
	Inbox      func(string) mediafiles.Inbox
	// Optional I/O consumers use the current WhatsApp client when nil.
	Downloader Downloader
	Groups     GroupLister
	Notify     func(jid, text string)
}

// Outbound preserves the shared recipient errors and partial-send contract
// while their implementation remains with the command's outbound path.
type Outbound struct {
	Recipient func(string) (types.JID, error)
	Classify  func(error) error
	Chunks    func(context.Context, []string, func(string) error) error
}

// Text supplies the message catalogue and privacy formatting. The transport
// decides when a notice or log is emitted; formatting stays at composition.
type Text struct {
	ViewOnce, MediaFailed string
	MediaTooLarge         func(int64) string
	MaskSender            func(string, bool, string) string
	MaskJID               func(string, string) string
	MaskChat              func(string) string
	Redact                func(string) string
}

// Identity keeps resolution shared with the command's /send path. It reads
// the same supplied device store and context as the transport did directly.
type Identity struct {
	Canonical func(context.Context, wastore.LIDStore, types.JID, types.JID) types.JID
	LID       func(context.Context, wastore.LIDStore, types.JID) types.JID
}
