package main

func (b *Bridge) newRestartAPI() *RestartAPI {
	return &RestartAPI{Available: func() bool { return b.restart != nil }, Restart: func() { b.restart() }}
}
