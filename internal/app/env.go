package app

import "os"

// Environment is the environment the bridge reads while it runs: the
// variables it looks up, and the part of them that the programs it starts
// get (childEnv). The binary hands New the process's own; tests give each
// bridge one of its own, so that bridges in tests that run side by side
// never see each other's.
type Environment interface {
	LookupEnv(name string) (string, bool)
	Environ() []string
}

// processEnv is the process's own environment, read each time it is asked:
// the bridge changes its own PATH as it loads brains (SetBridgePATH), and
// the programs it starts after that get the new one.
type processEnv struct{}

func (processEnv) LookupEnv(name string) (string, bool) { return os.LookupEnv(name) }
func (processEnv) Environ() []string                    { return os.Environ() }

// envOr is env, or the process's own environment when env is nil.
func envOr(env Environment) Environment {
	if env == nil {
		return processEnv{}
	}
	return env
}

// getenv is os.Getenv for env, the process's own when nil.
func getenv(env Environment, name string) string {
	v, _ := envOr(env).LookupEnv(name)
	return v
}
