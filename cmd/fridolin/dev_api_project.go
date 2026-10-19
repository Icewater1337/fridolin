package main

func (b *Bridge) newDevAPI() *DevAPI {
	return &DevAPI{
		HasBrain: func(id string) bool { return b.brainMgr.Get(id) != nil },
		Submit:   b.submitDevInbound,
		Messages: func() []DevMessage {
			rows := b.dev.Outbox()
			if rows == nil {
				return nil
			}
			out := make([]DevMessage, len(rows))
			for i, row := range rows {
				out[i] = DevMessage(row)
			}
			return out
		},
	}
}
