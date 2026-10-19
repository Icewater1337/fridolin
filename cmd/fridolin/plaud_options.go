package main

import (
	"context"
	"time"
)

// PlaudSettings is the integration's live configuration, including its dashboard shape.
// The command projection's explicit conversion checks that every field is represented.
type PlaudSettings struct {
	Enabled         bool   `json:"enabled"`
	Brain           string `json:"brain,omitempty"`
	CLIPath         string `json:"cli_path,omitempty"`
	IntervalSeconds int    `json:"interval_seconds,omitempty"`
	Polished        bool   `json:"polished,omitempty"`
	Message         string `json:"message,omitempty"`
	BrainAccess     bool   `json:"brain_access,omitempty"`
	PhoneDir        string `json:"phone_dir,omitempty"`
	PhoneUpload     bool   `json:"phone_upload,omitempty"`
}

// PlaudSnapshot keeps one poll's policy and paths together.
type PlaudSnapshot struct {
	Plaud             PlaudSettings
	DataDir, MediaDir string
	QueueMaxPending   int
	OwnerChat         string
	Interval          time.Duration
}

// PlaudOptions supplies startup paths and current settings without a config store.
type PlaudOptions struct {
	PlaudDir, Home string
	Settings       func() *PlaudSnapshot
	Target         func(id string) *PlaudTarget
	HeldTarget     func() string
	Notify         func(context.Context, string, string) error
}

// PlaudTarget retains the selected runtime for a handoff. Its callbacks read
// that runtime's current reply settings and submit to that same runtime even
// when a reload replaces it with another brain bearing the same ID.
type PlaudTarget struct {
	BrainID    string
	ReplyChat  func() string
	OtherOwner func(jid string) string
	Submit     func(jid, sender, text string, media []string, done func(error)) error
}

func (t *PlaudTarget) ID() string { return t.BrainID }
