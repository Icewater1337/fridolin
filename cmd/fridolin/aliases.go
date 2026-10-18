package main

import "github.com/Icewater1337/fridolin/internal/store"

// Temporary names until the callers use the store package directly.
type Store = store.Store
type sentPoll = store.SentPoll
type PollResult = store.PollResult
type PollOption = store.PollOption
type IgnoredChat = store.IgnoredChat
type outboxPart = store.OutboxPart
type outboxRow = store.OutboxRow
type outboxSummary = store.OutboxSummary

const outboxFailedKeep = store.OutboxFailedKeep

var NewStore = store.Open
