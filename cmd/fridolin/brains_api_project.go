package main

func (b *Bridge) newBrainsAPI() *BrainsAPI {
	return &BrainsAPI{Registry: func() BrainRegistry { return b.registry }, Views: b.brainViews}
}
