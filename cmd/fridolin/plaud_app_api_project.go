package main

func (b *Bridge) newPlaudAppAPI() *PlaudAppAPI {
	return &PlaudAppAPI{Plaud: func() PlaudAppService { return b.plaud }}
}
