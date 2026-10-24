package app

import (
	"context"
	"log"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/google"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

// calendarGrantsEvery is how often the bridge asks Google whether the
// brains' calendar grants still work, so that one revoked at Google shows
// before a brain needs it. The first check comes calendarGrantsFirst
// after the start.
const (
	calendarGrantsEvery = 6 * time.Hour
	calendarGrantsFirst = 2 * time.Minute
)

// watchCalendarGrants checks the grants of the configured brains every
// calendarGrantsEvery (google.CheckGrants).
func (b *App) watchCalendarGrants(ctx context.Context) {
	t := time.NewTimer(calendarGrantsFirst)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var ids []string
		for _, br := range b.brainMgr.Brains() {
			ids = append(ids, br.ID())
		}
		b.google.CheckGrants(ctx, ids)
		t.Reset(calendarGrantsEvery)
	}
}

// calendarGrantBroken tells the owner, in plain words and by the brain's
// name, that a brain's calendar stopped working (google.Options.OnBroken).
func (b *App) calendarGrantBroken(brainID, state string) {
	name := brainID
	if br := b.brainMgrGet(brainID); br != nil && br.Name() != "" {
		name = br.Name()
	}
	cfg := b.cfgs.Get()
	target := ownerChat(cfg)
	if target == "" {
		return
	}
	text := outboundmsg.CalendarGrantBrokenAlert(name, state == google.GrantRevoked)
	if err := b.outbox.Text(context.Background(), target, text, cfg.MaxChunkSize); err != nil {
		log.Printf("brain %s: telling the owner that its calendar stopped working: %v", brainID, err)
	}
}

// brainMgrGet is b.brainMgr.Get, or nil before the brains are loaded.
func (b *App) brainMgrGet(id string) *brain.Brain {
	if b.brainMgr == nil {
		return nil
	}
	return b.brainMgr.Get(id)
}
