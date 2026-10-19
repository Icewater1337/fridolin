package main

import (
	"github.com/Icewater1337/fridolin/internal/access"

	"github.com/Icewater1337/fridolin/internal/trigger"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// Whether a group message addresses the bridge. One that does is
// "tagged" and gets a visible reply; the rest is chatter, which the brain
// reads without answering.

// groupRules are the parts of a brain's routing that decide what
// addresses the bridge in the brain's groups (see BrainRouting).
type groupRules struct {
	files    bool // a file counts as addressing the bridge
	forwards bool // a forwarded message does
}

// defaultGroupRules are what a group without a brain, or a brain that
// sets neither, goes by: files and forwards address the bridge, as they
// always did.
var defaultGroupRules = groupRules{files: true, forwards: true}

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
func addressGroup(msg *waE2E.Message, text string, triggers trigger.List, own []types.JID, rules groupRules) groupAddressing {
	e := groupEnvelope(msg, text, own)
	a := access.AddressGroup(access.Policy{Triggers: triggers, Files: rules.files, Forwards: rules.forwards}, e)
	return groupAddressing{tagged: a.Tagged, mentioned: a.Mentioned, byCaption: a.ByCaption, file: a.File}
}
