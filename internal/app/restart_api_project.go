package app

import "github.com/Icewater1337/fridolin/internal/httpapi"

func (b *App) newRestartAPI() *httpapi.RestartAPI {
	return &httpapi.RestartAPI{Available: b.canRestart, Restart: func() { b.RequestRestart() }}
}
