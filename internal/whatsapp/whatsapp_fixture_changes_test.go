package whatsapp

import (
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// dmEvent is a DM from the allowed number, with the message ID id.
func dmEvent(id string, msg *waE2E.Message) *events.Message {
	chatJID := types.NewJID("15551234567", types.DefaultUserServer)
	return &events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chatJID, Sender: chatJID}, ID: types.MessageID(id), Timestamp: time.Now()},
		Message: msg,
	}
}

func textMsg(s string) *waE2E.Message { return &waE2E.Message{Conversation: proto.String(s)} }

// revokeMsg deletes the message id for everyone.
func revokeMsg(id string) *waE2E.Message {
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{ID: proto.String(id)},
	}}
}

// editMsg gives the message id the text s.
func editMsg(id, s string) *waE2E.Message {
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: &waCommon.MessageKey{ID: proto.String(id)}, EditedMessage: textMsg(s),
	}}
}
