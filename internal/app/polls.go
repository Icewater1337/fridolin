package app

import (
	"fmt"
	"log"

	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/types"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/store"
)

// pollVote is a vote the bridge recorded in one of its polls.
type pollVote struct {
	Poll     store.SentPoll
	Voter    types.JID // as canonicalUser has them
	Selected []string  // empty: the vote was taken back
}

// recordVote writes a vote into the history of the brain that sent the
// poll, as an event: the brain's next run in that chat sees who voted
// for what, without anyone asking it to look. Polls from before the
// bridge kept their brain go to the brain that owns their chat.
func (b *App) recordVote(v pollVote) {
	if id, ok := strings.CutPrefix(v.Poll.Ref, approvalRefPrefix); ok {
		b.approvalVote(id, v)
		return
	}
	if token, ok := strings.CutPrefix(v.Poll.Ref, ownerPickPrefix); ok {
		b.ownerPickVote(token, v)
		return
	}
	br := b.brainMgr.Get(v.Poll.Brain)
	if br == nil {
		br = b.brainMgr.BrainFor(v.Poll.Chat, chat.IsGroupJID(v.Poll.Chat))
	}
	if br == nil {
		log.Printf("poll vote in %s: no brain to tell", v.Poll.Chat)
		return
	}
	br.AppendHistory(v.Poll.Chat, v.Voter.User, voteText(v.Poll.Question, v.Selected), history.Event)
}

// voteText is how a vote reads in a brain's history, after the voter's
// name: `voted "Monday 2pm" in the poll "When should we meet?"`.
func voteText(question string, selected []string) string {
	if len(selected) == 0 {
		return fmt.Sprintf("took back their vote in the poll %q", question)
	}
	quoted := make([]string, len(selected))
	for i, s := range selected {
		quoted[i] = strconv.Quote(s)
	}
	return fmt.Sprintf("voted %s in the poll %q", strings.Join(quoted, ", "), question)
}
