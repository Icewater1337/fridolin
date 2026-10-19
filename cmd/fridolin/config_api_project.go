package main

import "github.com/Icewater1337/fridolin/internal/httpapi"

func (b *Bridge) newConfigAPI() *httpapi.ConfigAPI {
	return &httpapi.ConfigAPI{Store: func() httpapi.ConfigStore { return b.cfgs }}
}
