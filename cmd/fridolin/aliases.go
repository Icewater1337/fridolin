package main

import "github.com/Icewater1337/fridolin/internal/config"

var effective = config.Resolve
var boolOr = config.BoolOr
var intOr = config.IntOr
var homeDir = config.HomeDir
var isTruthy = config.IsTruthy
var classicModeRemoved = config.ClassicModeRemoved
var migrateConfigJSON = config.MigrateJSON

const currentConfigVersion = config.Version

var errConfigTooNew = config.ErrTooNew

const migrateBuild = config.MigrateBuild

var defaultDataDir = config.DefaultDataDir
var dotEnvPath = config.DotEnvPath
var resolveDataDir = config.ResolveDataDir
var dataDirSource = config.DataDirSource

const dataDirName = config.DataDirName
const legacyDataDirName = config.LegacyDataDirName
const dataDirFromConfig = config.DataDirFromConfig
const dataDirFromDefault = config.DataDirFromDefault
const dataDirFromLegacy = config.DataDirFromLegacy
const defaultHTTPListen = config.DefaultHTTPListen
const defaultQuietMS = config.DefaultQuietMS
const defaultBackupRemote = config.DefaultBackupRemote
const defaultBackupRoot = config.DefaultBackupRoot
const queueModeQueue = config.QueueModeQueue
const queueModeInterrupt = config.QueueModeInterrupt

var warnOddChats = config.WarnOddChats
var warnGroupsWithoutMembers = config.WarnGroupsWithoutMembers
var groupMemberPairs = config.GroupMemberPairs
var validateGroupMembers = config.ValidateGroupMembers
var groupMembersAsNumbers = config.GroupMembersAsNumbers
var allowListGroup = config.AllowListGroup
var isLoopbackAddr = config.IsLoopbackAddr

type Config = config.Config
type SandboxConfig = config.SandboxConfig
type QueueConfig = config.QueueConfig
type DebounceConfig = config.DebounceConfig
type IngestConfig = config.IngestConfig
type StartupChecksConfig = config.StartupChecksConfig
type DashboardConfig = config.DashboardConfig
type ScriptAPIConfig = config.ScriptAPIConfig
type BackupConfig = config.BackupConfig
type PlaudConfig = config.PlaudConfig
type BrainRef = config.BrainRef

var QueueModes = config.QueueModes
var GitConfigs = config.GitConfigs
var RetiredKeyNames = config.RetiredKeyNames
