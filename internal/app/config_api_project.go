package app

import "github.com/Icewater1337/fridolin/internal/httpapi"

func (b *App) newConfigAPI() *httpapi.ConfigAPI {
	return &httpapi.ConfigAPI{Store: func() httpapi.ConfigStore { return b.cfgs }}
}
