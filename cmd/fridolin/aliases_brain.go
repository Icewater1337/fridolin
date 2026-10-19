package main

import "github.com/Icewater1337/fridolin/internal/brain"

type Inbound = brain.Inbound
type Brain = brain.Brain
type brainManager = brain.Manager
type brainManagerOptions = brain.Options
type brainApplyOptions = brain.ApplyOptions
type brainSettings = brain.Settings
type brainGlobals = brain.Globals
type brainInputRules = brain.InputRules
type brainRunSpec = brain.RunSpec
type storedInbound = brain.SavedInbound
type BrainRuntime = brain.Runtime
type RunView = brain.RunView
type RunInfo = brain.RunInfo
type OTPCatch = brain.OTPCatch

const brainQueueInterrupt = brain.QueueInterrupt
const stopRunWait = brain.StopRunWait
const streamLogName = brain.StreamLogName

var newBrainManager = brain.NewManager
var mergeBrainSettings = brain.MergeSettings
var errBrainDropped = brain.ErrDropped
var errQueueFull = brain.ErrQueueFull
var errSandboxUnavailable = brain.ErrSandboxUnavailable
var errBrainsStopped = brain.ErrStopped
var dropped = brain.Dropped
var RunHeadingPID = brain.RunHeadingPID
