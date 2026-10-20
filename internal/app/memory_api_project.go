package app

import (
	"context"
	"fmt"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/intent"
	"github.com/Icewater1337/fridolin/internal/memindex"
	"github.com/Icewater1337/fridolin/internal/skill"
)

// brainMemory is httpapi.BrainMemoryService: the memory index and the
// standing intents, for a brain's runs.
type brainMemory struct{ b *App }

func (m brainMemory) Search(ctx context.Context, br *brainruntime.Brain, q string, limit int) (any, int, error) {
	hits, err := memindex.Search(ctx, memoryBrain(m.b.cfgs.Boot().Layout(), m.b.cfgs.Get(), br.ID(), br.Dir()), q, memindex.SearchOptions{Limit: limit})
	if hits == nil {
		hits = []memindex.Hit{}
	}
	return hits, len(hits), err
}

func (m brainMemory) Intents(br *brainruntime.Brain) (any, error) {
	return m.b.intents.List(br.ID())
}

// owner returns the run tok answers, when it is br's and answers the
// owner (ownerAsked).
func (m brainMemory) owner(tok string, br *brainruntime.Brain) (runInfo, error) {
	c, ok := m.b.runTokens.runOf(tok)
	if !ok || c.Brain != br.ID() || !ownerAsked(c, m.b.cfgs.Get()) {
		return c, intent.ErrNotOwner
	}
	// Setting or removing an intent writes (M5's policy): not in a
	// lockdown, nor in a run whose budget is read.
	if m.b.locked() {
		return c, apperr.New(apperr.Forbidden, "the bridge is in lockdown: only reading goes ahead, so no standing intent can be set or removed. Say so in your reply")
	}
	if c.Policy != nil && c.Policy.Risk.Rank() < skill.RiskWrite.Rank() {
		return c, apperr.New(apperr.Forbidden, fmt.Sprintf("this run's risk level is %s, which does not allow setting or removing a standing intent", c.Policy.Risk))
	}
	return c, nil
}

func (m brainMemory) AddIntent(tok string, br *brainruntime.Brain, req intent.Request) (any, error) {
	c, err := m.owner(tok, br)
	if err != nil {
		return nil, err
	}
	in, err := intent.Compile(req, c.Message.JID, chat.IdentityKey(c.Message.Sender), time.Now(), time.Local)
	if err != nil {
		return nil, err
	}
	return m.b.intents.Add(br.ID(), in)
}

func (m brainMemory) RemoveIntent(tok string, br *brainruntime.Brain, id string) error {
	if _, err := m.owner(tok, br); err != nil {
		return err
	}
	return m.b.intents.Remove(br.ID(), id)
}
