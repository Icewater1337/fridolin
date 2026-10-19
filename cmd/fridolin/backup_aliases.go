package main

import "github.com/Icewater1337/fridolin/internal/backup"

type BackupManager = backup.Manager
type BackupResult = backup.Result
type BackupSummary = backup.Summary

var NewBackupService = backup.New

const BackupBridgeID = backup.BridgeID

type BackupSettings = backup.Settings
type BackupTarget = backup.Target
type BackupTargetSettings = backup.TargetSettings
type BackupOptions = backup.Options
type BackupRunFunc = backup.RunFunc
type BackupBridgeFiles = backup.BridgeFiles
type BackupManifestFile = backup.ManifestFile
type BackupRestoreRequest = backup.RestoreRequest

var ErrBackupRestoreRequest = backup.ErrRestoreRequest
var ErrBackupRcloneMissing = backup.ErrRcloneMissing

type RestoreResult = backup.RestoreResult
