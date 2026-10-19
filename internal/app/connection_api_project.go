package app

import (
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

func (b *App) newConnectionAPI() *httpapi.ConnectionAPI {
	return &httpapi.ConnectionAPI{
		Connection:    func() httpapi.PairingConnection { return b.wa },
		AllowedGroups: func() []string { return b.cfgs.Effective().AllowedGroups },
		BrainFor:      func(jid string, group bool) *brain.Brain { return b.brainMgr.BrainFor(jid, group) },
		Brain:         func(id string) *brain.Brain { return b.brainMgr.Get(id) },
	}
}
