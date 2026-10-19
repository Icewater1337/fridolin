package main

import "github.com/Icewater1337/fridolin/internal/config"

type Store = config.Store
type Snapshot = config.Snapshot
type Tx = config.Tx
type UpdateResult = config.UpdateResult

var (
	ErrConflict        = config.ErrConflict
	Open               = config.Open
	Load               = config.Load
	LoadExisting       = config.LoadExisting
	Starter            = config.Starter
	WriteStarter       = config.WriteStarter
	EnsureFile         = config.EnsureFile
	StarterDataDir     = config.StarterDataDir
	DefaultPath        = config.DefaultPath
	MustClone          = config.MustClone
	DecodeReplacement  = config.DecodeReplacement
	LogSecurityChanges = config.LogSecurityChanges
	RestartFieldNames  = config.RestartFieldNames
)
