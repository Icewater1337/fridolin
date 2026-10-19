package main

import "github.com/Icewater1337/fridolin/internal/plaud"

type PlaudManager = plaud.Manager

var newPlaudManager = plaud.New

type PlaudOptions = plaud.Options
type PlaudSettings = plaud.Settings
type PlaudSnapshot = plaud.Snapshot
type PlaudTarget = plaud.Target
type PlaudRunFunc = plaud.RunFunc
type PlaudForward = plaud.Forward
type PlaudConnect = plaud.ConnectJob
type PlaudCLI = plaud.CLI
type PlaudStatus = plaud.Status
type PlaudAppStatus = plaud.AppStatus
type PlaudPhoneRow = plaud.PhoneRow
type plaudDeveloper = plaud.Developer

var plaudDecodeError = plaud.DecodeError

const plaudTranscriptMax = plaud.TranscriptMax

var errPlaudUnknownRecording = plaud.ErrUnknownRecording
var errPlaudAlreadyWaiting = plaud.ErrAlreadyWaiting
var errPlaudNotInstalled = plaud.ErrNotInstalled
var errPlaudSigningIn = plaud.ErrSigningIn
var errPlaudNotConnected = plaud.ErrNotConnected
var errPlaudSendingOff = plaud.ErrSendingOff
var errPlaudStopping = plaud.ErrStopping
var errPlaudAppBuilding = plaud.ErrAppBuilding
var errPlaudUnknownDevice = plaud.ErrUnknownDevice
