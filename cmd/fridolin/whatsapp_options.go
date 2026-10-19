package main

import (
	"context"

	"github.com/Icewater1337/fridolin/internal/access"
	"github.com/Icewater1337/fridolin/internal/chat"
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
}
