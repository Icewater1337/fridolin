package main

import (
	"os"
	"time"

	mediafiles "github.com/Icewater1337/fridolin/internal/media"
)

// processStarted is the cutoff for files left by older versions. The same
// migration lifecycle serializes first-inbox creation across config reloads.
var processStarted = time.Now()
var mediaMigration = mediafiles.Migration{Started: processStarted}

// mediaInboxAt supplies process state at the composition boundary. The media
// layer receives resolved settings and never reads Config or the environment.
func mediaInboxAt(dir string) mediafiles.Inbox {
	workingDir, _ := os.Getwd()
	return mediafiles.Inbox{Dir: dir, WorkingDir: workingDir, Migration: &mediaMigration}
}
