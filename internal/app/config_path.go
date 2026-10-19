package app

import (
	"log"
	"os"

	"github.com/Icewater1337/fridolin/internal/config"
)

// Where the config file came from, as the log and `fridolin paths` name it.
const (
	ConfigFromFlag    = "--config"
	ConfigFromEnv     = "FRIDOLIN_CONFIG"
	ConfigFromDefault = "default"
)

func ConfigPathFromPaths(flagValue string, paths config.Paths) (path, source string) {
	if old := os.Getenv("CONFIG_FILE"); old != "" {
		log.Printf("warning: CONFIG_FILE=%s is ignored: set FRIDOLIN_CONFIG instead, or pass --config", old)
	}
	if flagValue != "" {
		return flagValue, ConfigFromFlag
	}
	if p := os.Getenv("FRIDOLIN_CONFIG"); p != "" {
		return p, ConfigFromEnv
	}
	return config.DefaultPath(paths), ConfigFromDefault
}
