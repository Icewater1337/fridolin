package main

func (b *Bridge) newConfigAPI() *ConfigAPI {
	return &ConfigAPI{Store: func() ConfigStore { return b.cfgs }}
}
