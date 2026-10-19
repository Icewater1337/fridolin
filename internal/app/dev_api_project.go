package app

import "github.com/Icewater1337/fridolin/internal/httpapi"

func (b *App) newDevAPI() *httpapi.DevAPI {
	return &httpapi.DevAPI{
		HasBrain: func(id string) bool { return b.brainMgr.Get(id) != nil },
		Submit:   b.submitDevInbound,
		Messages: func() []httpapi.DevMessage {
			rows := b.dev.Outbox()
			if rows == nil {
				return nil
			}
			out := make([]httpapi.DevMessage, len(rows))
			for i, row := range rows {
				out[i] = httpapi.DevMessage(row)
			}
			return out
		},
	}
}
