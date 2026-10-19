package main

import "github.com/Icewater1337/fridolin/internal/httpapi"

func (b *Bridge) newPlaudAppAPI() *httpapi.PlaudAppAPI {
	return &httpapi.PlaudAppAPI{Plaud: func() httpapi.PlaudAppService { return b.plaud }}
}
