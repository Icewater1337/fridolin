package config

// Paths captures the process's home and chosen default data directory once.
// The caller chooses DefaultDataDir before resolution; Resolve never probes
// which legacy or current default directory happens to exist.
type Paths struct {
	Home           string
	DefaultDataDir string
}

// Options supplies process inputs at the composition boundary. Getenv is used
// only to reject the retired BRAIN_MODE switch when the store opens. The store
// retains Paths and GOOS for later resolution, not a live environment lookup.
type Options struct {
	Paths       Paths
	GOOS        string
	Getenv      func(string) string
	CheckBrains func(old, next *Config) error
}

func (o Options) getenv(name string) string {
	if o.Getenv == nil {
		return ""
	}
	return o.Getenv(name)
}
