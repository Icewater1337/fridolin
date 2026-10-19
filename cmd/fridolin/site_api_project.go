package main

import (
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"net/http"
)

func (b *Bridge) siteAPI(mux *http.ServeMux) {
	(&httpapi.SiteAPI{Site: func() httpapi.SiteService { return b.site }, StatusView: func() map[string]any { return b.site.StatusView() }, BuildersView: b.siteBuildersView, BuildRoutes: b.buildAPI}).Register(mux)
}
func (b *Bridge) siteBuildersView() []map[string]any {
	out := []map[string]any{}
	for _, br := range b.brainMgr.Brains() {
		item := map[string]any{"id": br.ID(), "name": br.Name(), "builder": b.site.IsBuilder(br.ID())}
		if urls := b.site.PreviewURLs(br.ID()); urls != nil {
			item["preview"] = urls
		}
		out = append(out, item)
	}
	return out
}
