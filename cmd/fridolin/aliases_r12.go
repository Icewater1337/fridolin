package main

import (
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/pipeline"
)

type PrincipalKind = chat.PrincipalKind
type Principal = chat.Principal

const (
	CallerAnonymous = chat.CallerAnonymous
	CallerScript    = chat.CallerScript
	CallerBrain     = chat.CallerBrain
)

type Debouncer = pipeline.Debouncer

var NewDebouncer = pipeline.NewDebouncer
var IsJunk = pipeline.IsJunk
