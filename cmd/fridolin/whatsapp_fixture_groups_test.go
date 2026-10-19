package main

import (
	"context"

	"sync/atomic"

	"go.mau.fi/whatsmeow/types"
)

// fakeLister is a logged-in whatsmeow client as far as the group queries
// go: it counts the joined-groups queries and answers each with one group,
// and counts the group-info queries, which answer with the name info, or
// fail with infoErr, or, with hang set, wait for their context to end.
type fakeLister struct {
	calls     atomic.Int32
	infoCalls atomic.Int32
	info      string
	infoErr   error
	hang      bool
}

func (*fakeLister) IsLoggedIn() bool { return true }

func (f *fakeLister) GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error) {
	f.infoCalls.Add(1)
	if f.hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.infoErr != nil {
		return nil, f.infoErr
	}
	return &types.GroupInfo{JID: jid, GroupName: types.GroupName{Name: f.info}}, nil
}

func (f *fakeLister) GetJoinedGroups(context.Context) ([]*types.GroupInfo, error) {
	f.calls.Add(1)
	return []*types.GroupInfo{{
		JID:          types.NewJID("120363000000000000", types.GroupServer),
		GroupName:    types.GroupName{Name: "Family"},
		Participants: make([]types.GroupParticipant, 3),
	}}, nil
}
