package main

import "github.com/Icewater1337/fridolin/internal/media"

// Temporary extraction aliases; the next commit rewrites their callers.
type MediaInbox = media.Inbox
type MediaMigration = media.Migration
type mediaFile = media.File

const (
	incomingMediaDir    = media.IncomingDir
	mediaInboxesMarker  = media.InboxesMarker
	legacyMediaDir      = media.LegacyDir
	mediaStuckFile      = media.StuckFile
	mediaCipherOverhead = media.CipherOverhead
)

var (
	errNotTheInbox   = media.ErrNotTheInbox
	errMediaTooLarge = media.ErrTooLarge
)
