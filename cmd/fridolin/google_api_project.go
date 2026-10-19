package main

import "github.com/Icewater1337/fridolin/internal/brain"

func (b *Bridge) newGoogleAPI() *GoogleAPI {
	return &GoogleAPI{Google: func() GoogleService { return b.google }, Brain: func(id string) *brain.Brain { return b.brainMgr.Get(id) }}
}
