package main

import "github.com/Icewater1337/fridolin/internal/backup"

var BackupSecretExcludes = backup.SecretExcludes
var BackupFilterLines = backup.FilterLines
var BackupBrainFilter = backup.BrainFilter

const BackupDayLayout = backup.DayLayout

var BackupDue = backup.Due
var BackupRcloneSection = backup.RcloneSection
var BackupVersionName = backup.VersionName
var BackupParseVersion = backup.ParseVersion
var BackupExpiredVersions = backup.ExpiredVersions
var BackupSortVersions = backup.SortVersions
var BackupNameOK = backup.NameOK
var BackupVersionOK = backup.VersionOK
