package main

import (
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/history"
)

// Temporary extraction aliases, removed in the following mechanical commit.
type historyEntry = history.Entry

const (
	historyIn       = history.In
	historyOut      = history.Out
	historyEvent    = history.Event
	historyReset    = history.Reset
	historyFileName = history.FileName
)

var (
	scanHistoryFile    = history.Scan
	recentHistoryFile  = history.Tail
	historyForChat     = history.ForChat
	appendHistoryEntry = history.Append
	rewriteHistoryFile = history.Rewrite
	holdsEntryFile     = history.HoldsEntry
	openInFolder       = fsx.OpenInFolder
	errSymlink         = fsx.ErrSymlink
)
