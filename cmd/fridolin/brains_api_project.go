package main

import "github.com/Icewater1337/fridolin/internal/httpapi"

func (b *Bridge) newBrainsAPI() *httpapi.BrainsAPI {
	return &httpapi.BrainsAPI{Registry: func() httpapi.BrainRegistry { return b.registry }, Views: b.brainViews}
}
