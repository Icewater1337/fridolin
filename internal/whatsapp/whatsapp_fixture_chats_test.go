package whatsapp

import (
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

const (
	testDMChat = "15551234567@s.whatsapp.net"
	testGroup  = "120363000000000000@g.us"
	// otherNumber is a second person: another member of the group, or the
	// bridge's own account as a quote names it.
	otherNumber = "15559876543"
)

// groupEvent is msg, sent by the allowed number in the allowed group.
func groupEvent(msg *waE2E.Message) *events.Message {
	src := types.MessageSource{
		Chat:    types.NewJID("120363000000000000", types.GroupServer),
		Sender:  types.NewJID("15551234567", types.DefaultUserServer),
		IsGroup: true,
	}
	return &events.Message{Info: types.MessageInfo{MessageSource: src, ID: "3EB0GROUP", Timestamp: time.Now()}, Message: msg}
}

// bridgeLID is a deliberately short fixture identifier, never a real LID.
const bridgeLID = "424242"
