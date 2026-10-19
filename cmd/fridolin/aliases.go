package main

import "github.com/Icewater1337/fridolin/internal/manifest"

type BrainManifest = manifest.Manifest
type BrainRouting = manifest.Routing
type BrainBackup = manifest.Backup
type BrainCalendar = manifest.Calendar
type BrainSpec = manifest.Spec

const ManifestFile = manifest.File
const defaultPromptFile = manifest.DefaultPromptFile
const manifestVersion = manifest.Version

var ReadManifest = manifest.Read
var WriteManifest = manifest.Write
var errManifestExists = manifest.ErrExists
var errManifestTooNew = manifest.ErrTooNew
var slugify = manifest.Slugify
var decodeManifest = manifest.Decode
var encodeManifest = manifest.Encode
var openManifestRoot = manifest.OpenRoot
var writeManifestDataRoot = manifest.WriteDataRoot

const observeHistory = manifest.ObserveHistory
const observeRun = manifest.ObserveRun

var DefaultBackupExcludes = manifest.DefaultBackupExcludes
var ValidID = manifest.ValidID
var ValidEffort = manifest.ValidEffort
var Efforts = manifest.Efforts
var ValidObserve = manifest.ValidObserve
var ObserveModes = manifest.ObserveModes
