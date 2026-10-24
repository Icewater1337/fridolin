package app

import (
	"context"
	"log"
	"slices"
	"sync"
	"time"

	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/intent"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/memindex"
	prompttext "github.com/Icewater1337/fridolin/internal/prompt"
)

// memoryBrain is the memory index's view of brain id, whose folder is dir.
func memoryBrain(layout config.Layout, cfg *config.Settings, id, dir string) memindex.Brain {
	return memindex.Brain{ID: id, Dir: dir, Index: layout.MemoryIndex(id), Owners: slices.Clone(cfg.AllowedNumbers)}
}

// recallTimeout bounds what the index may add to a run's start: a sync of
// a large history the first time, then milliseconds.
const recallTimeout = 20 * time.Second

// recall is BrainHooks.Recall: for a brain whose manifest turns
// memory.inject on, what its memory index recalls for the message, minus
// what the prompt shows anyway.
func (b *App) recall(r brainruntime.RecallRequest) []prompttext.Recalled {
	mem := manifest.MemoryOf(r.Manifest)
	if !mem.Inject {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), recallTimeout)
	defer cancel()
	hits, err := memindex.Recall(ctx, memoryBrain(b.cfgs.Boot().Layout(), b.cfgs.Get(), r.ID, r.Dir), r.Message.TextWithQuote(),
		memindex.RecallOptions{TopK: mem.TopK, MaxChars: mem.MaxChars, Exclude: shownExcluder(r.Message, r.Shown)})
	if err != nil {
		log.Printf("brain %s: memory index: nothing recalled for this run: %v", r.ID, err)
		return nil
	}
	return recalledOf(hits)
}

// recalledOf is hits as the prompt takes them.
func recalledOf(hits []memindex.Hit) []prompttext.Recalled {
	out := make([]prompttext.Recalled, 0, len(hits))
	for _, h := range hits {
		out = append(out, prompttext.Recalled{Source: h.Ref(), Provenance: string(h.Provenance), At: h.TS, Text: h.Text})
	}
	return out
}

// shownExcluder leaves out of a recall the history lines the prompt has
// already: the new message and the chat's recent ones, by their WhatsApp
// id, or, for lines without one, as lines of the chat from the oldest
// shown (or the new message's second) on.
func shownExcluder(m chat.Inbound, shown []history.Entry) func(memindex.Hit) bool {
	ids := map[string]bool{}
	if m.ID != "" {
		ids[m.ID] = true
	}
	from := m.At.Truncate(time.Second)
	for _, e := range shown {
		if e.ID != "" {
			ids[e.ID] = true
		}
		if t, err := time.Parse(time.RFC3339, e.TS); err == nil && (from.IsZero() || t.Before(from)) {
			from = t
		}
	}
	return func(h memindex.Hit) bool {
		if h.Path != memindex.HistoryFile {
			return false
		}
		if ids[h.Anchor] {
			return true
		}
		if !memindex.LineAnchor(h.Anchor) {
			return false // a WhatsApp message, shown or not by its id
		}
		return (h.Chat == m.JID || (m.ChatAlt != "" && h.Chat == m.ChatAlt)) && !from.IsZero() && !h.TS.Before(from)
	}
}

// ownerAsked reports whether the run c answers its owner: a message typed
// in a direct chat (WhatsApp, or the dashboard's dev page, which only the
// dashboard's owner reaches) by an allowed number, addressed to the
// bridge. Standing intents are set only in such runs: not for a group
// member, a file handed in, a schedule that fired or chatter.
func ownerAsked(c runInfo, cfg *config.Settings) bool {
	m := c.Message
	if m.Silent || m.IsGroup() || m.Brain != "" || (m.Source != chat.SourceWhatsApp && m.Source != chat.SourceDev) {
		return false
	}
	sender := chat.IdentityKey(m.Sender)
	return sender != "" && slices.ContainsFunc(cfg.AllowedNumbers, func(n string) bool { return chat.IdentityKey(n) == sender })
}

// scheduler fires brains' standing intents and starts their scheduled
// agents.
type scheduler struct {
	b     *App
	every time.Duration
	mu    sync.Mutex
	// cancel stops the loop Start began; stopped refuses a Start after
	// Stop.
	cancel  context.CancelFunc
	stopped bool
	wg      sync.WaitGroup
}

// schedulerEvery is how often the scheduler looks for what fell due.
var schedulerEvery = 30 * time.Second

func (s *scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.cancel != nil {
		return
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Go(func() {
		t := time.NewTicker(s.every)
		defer t.Stop()
		for {
			s.tick(time.Now())
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	})
}

func (s *scheduler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopped = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// tick fires what fell due by now, for every brain that runs.
func (s *scheduler) tick(now time.Time) {
	b := s.b
	for _, br := range b.brainMgr.Brains() {
		id := br.ID()
		due, err := b.intents.Due(id, now)
		if err != nil {
			log.Printf("brain %s: standing intents: %v", id, err)
		}
		for _, in := range due {
			b.fireSchedule(br, in.Chat, intent.SenderIntent, intent.Message(in, "", "", ""))
		}
		if usesMemoryIndex(br.Manifest()) {
			// Kept up to date between runs too, for the doctor and for
			// searches from outside.
			ctx, cancel := context.WithTimeout(context.Background(), recallTimeout)
			if _, err := memindex.Sync(ctx, memoryBrain(b.cfgs.Boot().Layout(), b.cfgs.Get(), id, br.Dir())); err != nil {
				log.Printf("brain %s: memory index: %v", id, err)
			}
			cancel()
		}
	}
	// The scheduled agents, the memory consolidation among them.
	b.agents.tick(now)
	b.programs.sync()
}

// usesMemoryIndex reports whether a brain's memory index is kept up to
// date between runs: it recalls into prompts (memory.inject), or a
// scheduled agent consolidates its memory.
func usesMemoryIndex(m manifest.Manifest) bool {
	return manifest.MemoryOf(m).Inject || slices.ContainsFunc(m.EffectiveSchedules(), func(s manifest.Schedule) bool {
		return s.Template == manifest.ConsolidationSchedule && s.IsEnabled()
	})
}

// fireSchedule hands br text from the bridge's schedules, answered in
// jid.
func (b *App) fireSchedule(br *brainruntime.Brain, jid, sender, text string) {
	if jid == "" {
		log.Printf("brain %s: a schedule fell due, but the brain has no chat to answer in — set reply_chat in its settings", br.ID())
		return
	}
	log.Printf("brain %s: %s fell due", br.ID(), sender)
	m := brainruntime.Inbound{Inbound: chat.Inbound{JID: jid, Brain: br.ID(), Sender: sender, Text: text, Source: chat.SourceSchedule, At: time.Now()}}
	go b.handleInbound(context.Background(), m)
}

// observeIntents fires br's keyword intents that a message written into
// its history matches (BrainHooks.OnHistory): what someone wrote, never
// what the brain or the bridge did, nor the owner in the chat the intent
// answers in, where they set it.
func (b *App) observeIntents(id string, e history.Entry) {
	if e.Type != history.In || memindex.HandOffSenders[e.From] || !b.intents.HasMatch(id) {
		return
	}
	br := b.brainMgr.Get(id)
	if br == nil {
		return
	}
	fired, err := b.intents.Matching(id, e.Text, time.Now(), func(in intent.Intent) bool {
		return e.Chat == in.Chat && chat.IdentityKey(e.From) == chat.IdentityKey(in.CreatedBy)
	})
	if err != nil {
		log.Printf("brain %s: standing intents: %v", id, err)
	}
	for _, in := range fired {
		b.fireSchedule(br, in.Chat, intent.SenderIntent, intent.Message(in, e.From, e.Chat, e.Text))
	}
}
