package cli

import (
	"github.com/Icewater1337/fridolin/internal/toolcli"
	"os"
	"time"
)

// processToolIO is the real process's.
func processToolIO() toolcli.IO {
	dir, _ := os.Getwd()
	return toolcli.IO{Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv, Dir: dir, Now: time.Now()}
}
