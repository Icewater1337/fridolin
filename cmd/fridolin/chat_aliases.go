package main

import "github.com/Icewater1337/fridolin/internal/chat"

type InboundContent = chat.Inbound
type Quote = chat.Quote
type LinkState = chat.LinkState
type Group = chat.Group
type Change = chat.Change

const (
	SourceWhatsApp = chat.SourceWhatsApp
	SourceIngest   = chat.SourceIngest
	SourceDev      = chat.SourceDev
	ChangeEdit     = chat.ChangeEdit
	ChangeRevoke   = chat.ChangeRevoke
)

var (
	IsGroupJID   = chat.IsGroupJID
	DMJID        = chat.DMJID
	HistoryText  = chat.HistoryText
	NormalizeJID = chat.NormalizeJID
)
