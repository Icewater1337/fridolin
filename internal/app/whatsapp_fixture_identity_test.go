package app

import (
	"context"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// personLID is the LID of the allowed number, 15551234567, in tests.
const personLID = "171717"

// personLIDs is whatsmeow's LID store as far as it knows the allowed
// number: its LID and back.
type personLIDs struct{ store.LIDStore }

func (personLIDs) GetPNForLID(_ context.Context, lid types.JID) (types.JID, error) {
	if lid.User == personLID {
		return types.NewJID("15551234567", types.DefaultUserServer), nil
	}
	return types.EmptyJID, nil
}

func (personLIDs) GetLIDForPN(_ context.Context, pn types.JID) (types.JID, error) {
	if pn.User == "15551234567" {
		return types.NewJID(personLID, types.HiddenUserServer), nil
	}
	return types.EmptyJID, nil
}

// storedLIDs is personLIDs as whatsmeow's SQL store answers: a lookup
// under a context that ended fails, as the query under it does.
type storedLIDs struct{ personLIDs }

func (s storedLIDs) GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error) {
	if err := ctx.Err(); err != nil {
		return types.EmptyJID, err
	}
	return s.personLIDs.GetPNForLID(ctx, lid)
}

func (s storedLIDs) GetLIDForPN(ctx context.Context, pn types.JID) (types.JID, error) {
	if err := ctx.Err(); err != nil {
		return types.EmptyJID, err
	}
	return s.personLIDs.GetLIDForPN(ctx, pn)
}
