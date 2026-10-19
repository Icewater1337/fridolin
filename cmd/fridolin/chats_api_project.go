package main

import (
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

func (b *Bridge) newChatsAPI() *httpapi.ChatsAPI {
	return &httpapi.ChatsAPI{
		ListIgnored: func() ([]httpapi.IgnoredChatResponse, error) {
			rows, err := b.store.ListIgnored()
			if rows == nil {
				return nil, err
			}
			out := make([]httpapi.IgnoredChatResponse, len(rows))
			for i, row := range rows {
				out[i] = httpapi.IgnoredChatResponse(row)
			}
			return out, err
		},
		DismissIgnored: func(jid string, remove bool) error { return b.store.DismissIgnored(jid, remove) },
		AllowChat:      func(dm bool, entry, id string) error { return b.registry.AllowChat(dm, entry, id) },
		UpdateConfig:   func(update func(*config.File) error) error { _, err := b.cfgs.Update("", update); return err },
	}
}
