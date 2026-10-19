package main

import "github.com/Icewater1337/fridolin/internal/httpapi"

func (b *Bridge) newRestartAPI() *httpapi.RestartAPI {
	return &httpapi.RestartAPI{Available: func() bool { return b.restart != nil }, Restart: func() { b.restart() }}
}
