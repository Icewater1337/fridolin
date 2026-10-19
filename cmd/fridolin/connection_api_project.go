package main

import "github.com/Icewater1337/fridolin/internal/brain"

func (b *Bridge) newConnectionAPI() *ConnectionAPI {
	return &ConnectionAPI{
		Connection:    func() PairingConnection { return b.wa },
		AllowedGroups: func() []string { return b.cfgs.Get().AllowedGroups },
		BrainFor:      func(jid string, group bool) *brain.Brain { return b.brainMgr.BrainFor(jid, group) },
		Brain:         func(id string) *brain.Brain { return b.brainMgr.Get(id) },
	}
}
