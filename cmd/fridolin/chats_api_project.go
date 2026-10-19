package main

import "github.com/Icewater1337/fridolin/internal/config"

func (b *Bridge) newChatsAPI() *ChatsAPI {
	return &ChatsAPI{
		ListIgnored: func() ([]IgnoredChatResponse, error) {
			rows, err := b.store.ListIgnored()
			if rows == nil {
				return nil, err
			}
			out := make([]IgnoredChatResponse, len(rows))
			for i, row := range rows {
				out[i] = IgnoredChatResponse(row)
			}
			return out, err
		},
		DismissIgnored: func(jid string, remove bool) error { return b.store.DismissIgnored(jid, remove) },
		AllowChat:      func(dm bool, entry, id string) error { return b.registry.AllowChat(dm, entry, id) },
		UpdateConfig:   func(update func(*config.Config) error) error { _, err := b.cfgs.Update("", update); return err },
	}
}
