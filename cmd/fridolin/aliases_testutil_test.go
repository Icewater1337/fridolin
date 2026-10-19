package main

import "github.com/Icewater1337/fridolin/internal/testutil"

var sharedTestMain = testutil.Main

type sharedMainOptions = testutil.MainOptions
type sharedTestPaths = testutil.Paths

var fakeNewMessage = testutil.NewMessage

const fakePromptMark = testutil.PromptMark

var waitFor = testutil.WaitFor
var clearEnv = testutil.ClearEnv
var sharedAssertGolden = testutil.AssertGolden
var lineDiff = testutil.LineDiff
var linkTestdata = testutil.LinkTestdata

type fakePlaudRecording = testutil.PlaudRecording
type fakePlaudBlock = testutil.PlaudBlock
type fakePlaudSegment = testutil.PlaudSegment

var fakePlaudTranscript = testutil.PlaudTranscript

var bridgeVariables = testutil.BridgeVariables
