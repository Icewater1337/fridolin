package main

import (
	"os"
	"time"
)

// processStarted is the cutoff for files left by older versions. The same
// migration lifecycle serializes first-inbox creation across config reloads.
var processStarted = time.Now()
var mediaMigration = MediaMigration{Started: processStarted}

// mediaInboxAt supplies process state at the composition boundary. The media
// layer receives resolved settings and never reads Config or the environment.
func mediaInboxAt(dir string) MediaInbox {
	workingDir, _ := os.Getwd()
	return MediaInbox{Dir: dir, WorkingDir: workingDir, Migration: &mediaMigration}
}
