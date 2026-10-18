package main

import "github.com/Icewater1337/fridolin/internal/logx"

// Temporary names while the log package's call sites move.
type LogRing = logx.Ring
type LogLine = logx.Line

var NewLogRing = logx.NewRing
var newWaLogger = logx.NewWaLogger
