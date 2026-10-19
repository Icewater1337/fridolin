package main

import outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"

var ChunkMessage = outboundmsg.Chunk
var extractPolls = outboundmsg.ExtractPolls
var sendChunks = outboundmsg.SendChunks
var resetReply = outboundmsg.ResetReply
var stopReply = outboundmsg.StopReply
var helpReply = outboundmsg.HelpReply
var queuedNotice = outboundmsg.QueuedNotice
var queueFullNotice = outboundmsg.QueueFullNotice
var chatErrSandbox = outboundmsg.ChatErrSandbox
var chatErrClaude = outboundmsg.ChatErrClaude
var chatErrCodex = outboundmsg.ChatErrCodex
var buildChatError = outboundmsg.BuildChatError
var mediaTooLargeNotice = outboundmsg.MediaTooLargeNotice
var pollAsText = outboundmsg.PollAsText
var startupCheckAlert = outboundmsg.StartupCheckAlert
var backupRefusedAlert = outboundmsg.BackupRefusedAlert
var plaudGivenUpAlert = outboundmsg.PlaudGivenUpAlert

type pollData = outboundmsg.Poll
type partialSendError = outboundmsg.PartialSendError
type outboundLink = outboundmsg.Link

const defaultChunkSize = outboundmsg.DefaultChunkSize
const minChunkSize = outboundmsg.MinChunkSize
const noBrainNotice = outboundmsg.NoBrainNotice
const heldBrainNotice = outboundmsg.HeldBrainNotice
const otpCaughtNotice = outboundmsg.OtpCaughtNotice
const workingNotice = outboundmsg.WorkingNotice
const questionPrefix = outboundmsg.QuestionPrefix
const chatErrCodexNotSignedIn = outboundmsg.ChatErrCodexNotSignedIn
const chatErrCodexLimit = outboundmsg.ChatErrCodexLimit
const chatErrNotSignedIn = outboundmsg.ChatErrNotSignedIn
const chatErrLimit = outboundmsg.ChatErrLimit
const chatErrTimeout = outboundmsg.ChatErrTimeout
const chatErrGeneric = outboundmsg.ChatErrGeneric
const viewOnceNotice = outboundmsg.ViewOnceNotice
const mediaFailedNotice = outboundmsg.MediaFailedNotice
const plaudExpiredAlert = outboundmsg.PlaudExpiredAlert
const restartedNotice = outboundmsg.RestartedNotice

var errOffline = outboundmsg.ErrOffline
var errBadRecipient = outboundmsg.ErrBadRecipient
var errRefusedRecipient = outboundmsg.ErrRefusedRecipient
