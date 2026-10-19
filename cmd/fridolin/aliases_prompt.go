package main

import (
	"github.com/Icewater1337/fridolin/internal/chat"
	prompttext "github.com/Icewater1337/fridolin/internal/prompt"
)

type promptInput = prompttext.Input

var renderPrompt = prompttext.Build
var parseIdentityMap = prompttext.ParseIdentityMap

const identityMapFile = prompttext.IdentityMapPath
const dmHistoryWindow = prompttext.DMHistoryWindow
const groupHistoryWindow = prompttext.GroupHistoryWindow

var promptHistoryLimit = prompttext.HistoryLimit

const askUserQuestionNote = prompttext.AskUserQuestionNote
const observeOnlyNote = prompttext.ObserveOnlyNote

var outboundSender = prompttext.OutboundSender
var phoneNumber = chat.PhoneNumber
var identityKey = chat.IdentityKey
var canonicalPhoneNumber = chat.IsPhoneNumber
