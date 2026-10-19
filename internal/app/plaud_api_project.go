package app

import (
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

func (b *App) newPlaudAPI() *httpapi.PlaudAPI {
	return &httpapi.PlaudAPI{
		Plaud:        func() httpapi.PlaudService { return b.plaud },
		CurrentBrain: func() string { return b.cfgs.Effective().Plaud.Brain },
		HasBrain:     func(id string) bool { return b.brainMgr.Get(id) != nil },
		UpdateConfig: func(update func(*config.File) error) error { _, err := b.cfgs.Update("", update); return err },
	}
}
