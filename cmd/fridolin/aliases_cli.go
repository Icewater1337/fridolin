package main

import "github.com/Icewater1337/fridolin/internal/claudecli"

type cliOptions = claudecli.Options

var buildClaudeArgs = claudecli.Args
var buildCodexArgs = claudecli.CodexArgs
var claudeArgList = claudecli.ArgList

type claudeRun = claudecli.Spawn
type claudeProcess = claudecli.Process

var startClaude = claudecli.Start

type cliInvocation = claudecli.Invocation
type cliCallbacks = claudecli.Callbacks

var superviseCLI = claudecli.Run

type RunResult = claudecli.Result
type StreamSink = claudecli.StreamSink

var parseStream = claudecli.Parse
var parseAgentStream = claudecli.ParseProvider
var extractAskQuestion = claudecli.AskQuestion

const maxStreamLine = claudecli.MaxStreamLine

type cliProber = claudecli.Prober
type claudeStatus = claudecli.Status

const testedClaudeVersion = claudecli.TestedClaudeVersion

var parseClaudeVersion = claudecli.ParseClaudeVersion
var claudeVersionWarning = claudecli.VersionWarning

type cliError = claudecli.Error

var errBrainInterrupted = claudecli.ErrInterrupted
var errNoResult = claudecli.ErrNoResult
var errRunTimedOut = claudecli.ErrTimedOut
var errRunPanicked = claudecli.ErrPanicked
var runTimedOut = claudecli.TimedOut

const defaultCodexModel = claudecli.DefaultCodexModel

var agentName = claudecli.AgentName
var validateCodexTools = claudecli.ValidateCodexTools
