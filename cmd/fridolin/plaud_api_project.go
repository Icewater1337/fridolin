package main

import "github.com/Icewater1337/fridolin/internal/config"

func (b *Bridge) newPlaudAPI() *PlaudAPI {
	return &PlaudAPI{
		Plaud:        func() PlaudService { return b.plaud },
		CurrentBrain: func() string { return b.cfgs.Get().Plaud.Brain },
		HasBrain:     func(id string) bool { return b.brainMgr.Get(id) != nil },
		UpdateConfig: func(update func(*config.Config) error) error { _, err := b.cfgs.Update("", update); return err },
	}
}
