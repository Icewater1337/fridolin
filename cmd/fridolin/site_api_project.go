package main

import (
	"github.com/Icewater1337/fridolin/internal/httpapi"
	sitesvc "github.com/Icewater1337/fridolin/internal/site"
	"net/http"
)

func (b *Bridge) siteAPI(mux *http.ServeMux) {
	(&httpapi.SiteAPI{Site: func() httpapi.SiteService { return b.site }, StatusView: func() sitesvc.StatusSnapshot { return b.site.StatusView() }, BuildersView: b.siteBuildersView, BuildRoutes: b.buildAPI}).Register(mux)
}
func (b *Bridge) siteBuildersView() []httpapi.BuilderRow {
	out := []httpapi.BuilderRow{}
	for _, br := range b.brainMgr.Brains() {
		item := httpapi.BuilderRow{ID: br.ID(), Name: br.Name(), Builder: b.site.IsBuilder(br.ID())}
		if urls := b.site.PreviewURLs(br.ID()); urls != nil {
			item.Preview = &urls
		}
		out = append(out, item)
	}
	return out
}
