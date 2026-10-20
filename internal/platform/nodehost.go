package platform

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// NodeStateDir is where a node agent keeps its key and config on this
// system: %LOCALAPPDATA%\fridolin-node on Windows, ~/Library/Application
// Support/fridolin-node on macOS, and $XDG_STATE_HOME/fridolin-node
// (default ~/.local/state/fridolin-node) elsewhere. It is not the bridge's
// data dir: a node and the core may run on one machine.
func NodeStateDir() (string, error) {
	return nodeStateDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
}

func nodeStateDir(goos string, getenv func(string) string, home func() (string, error)) (string, error) {
	const name = "fridolin-node"
	switch goos {
	case "windows":
		if d := getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, name), nil
		}
		h, err := home()
		if err != nil || h == "" {
			return "", errors.New("neither %LOCALAPPDATA% nor a home folder is known")
		}
		return filepath.Join(h, "AppData", "Local", name), nil
	case "darwin":
		h, err := home()
		if err != nil || h == "" {
			return "", errors.New("no home folder is known")
		}
		return filepath.Join(h, "Library", "Application Support", name), nil
	default:
		if d := getenv("XDG_STATE_HOME"); d != "" && filepath.IsAbs(d) {
			return filepath.Join(d, name), nil
		}
		h, err := home()
		if err != nil || h == "" {
			return "", errors.New("neither $XDG_STATE_HOME nor a home folder is known")
		}
		return filepath.Join(h, ".local", "state", name), nil
	}
}
