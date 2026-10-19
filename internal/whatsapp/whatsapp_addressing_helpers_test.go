package whatsapp

import (
	"github.com/Icewater1337/fridolin/internal/access"

	"github.com/Icewater1337/fridolin/internal/trigger"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// groupAddressing is how a group message addresses the bridge.
type groupAddressing struct {
	tagged    bool // the bridge answers it
	mentioned bool // it @-mentions the bridge, whose mention is then stripped
	byCaption bool // the trigger is in an image's or document's caption, not in the text
	file      bool // it carries a file that counts as one (see attachmentOf)
}

// addressGroup decides whether msg, with the text text, addresses the
// bridge, whose own JIDs (number and LID) are own. A trigger in the text
// or in an image's or document's caption does, and so does an @-mention
// of the bridge or a quote-reply to one of its messages; a file and a
// forward do as rules say. Video captions do not count: they are the
// video's metadata, and a video is a file.
func addressGroup(msg *waE2E.Message, text string, triggers trigger.List, own []types.JID, rules testGroupRules) groupAddressing {
	e := groupEnvelope(msg, text, own)
	a := access.AddressGroup(access.Policy{Triggers: triggers, Files: rules.files, Forwards: rules.forwards}, e)
	return groupAddressing{tagged: a.Tagged, mentioned: a.Mentioned, byCaption: a.ByCaption, file: a.File}
}
