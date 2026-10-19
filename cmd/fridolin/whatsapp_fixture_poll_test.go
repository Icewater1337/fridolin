package main

import (
	"context"

	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// pollSecrets is whatsmeow's store of message secrets, as far as it keeps
// key as every poll's. Like the SQL store, it fails under a context that
// ended.
type pollSecrets struct {
	wastore.MsgSecretStore
	key []byte
}

func (s pollSecrets) GetMessageSecret(ctx context.Context, _, sender types.JID, _ types.MessageID) ([]byte, types.JID, error) {
	if err := ctx.Err(); err != nil {
		return nil, types.EmptyJID, err
	}
	return s.key, sender, nil
}

// pollVoteEvent is voter's vote for options in the poll the bridge sent
// voter as pollID, encrypted with key the way the voter's phone does.
func pollVoteEvent(t *testing.T, key []byte, voter types.JID, pollID string, options ...string) *events.Message {
	t.Helper()
	phone := whatsmeow.NewClient(&wastore.Device{ID: &voter, MsgSecrets: pollSecrets{key: key}}, waLog.Noop)
	// The poll as the phone has it: in the chat with the bridge, from it.
	poll := types.MessageInfo{MessageSource: types.MessageSource{Chat: bridgeAccount, Sender: bridgeAccount}, ID: types.MessageID(pollID)}
	vote, err := phone.EncryptPollVote(context.Background(), &poll, &waE2E.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions(options)})
	if err != nil {
		t.Fatal(err)
	}
	src := types.MessageSource{Chat: voter, Sender: voter}
	return &events.Message{Info: types.MessageInfo{MessageSource: src, ID: "3EB0VOTE", Timestamp: time.Now()}, Message: &waE2E.Message{PollUpdateMessage: vote}}
}
