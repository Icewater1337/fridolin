package app

import (
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

func (b *App) newGoogleAPI() *httpapi.GoogleAPI {
	return &httpapi.GoogleAPI{Google: func() httpapi.GoogleService { return b.google }, Brain: func(id string) *brain.Brain { return b.brainMgr.Get(id) }}
}
