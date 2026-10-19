package main

import (
	"context"
	"github.com/Icewater1337/fridolin/internal/google"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	prompttext "github.com/Icewater1337/fridolin/internal/prompt"
)

// commandRunner composes provider arguments, host environment and sandbox
// policy. The runtime supplies every process and output lifecycle callback.
type commandRunner struct {
	sandbox *SandboxProfiler
	hooks   *BrainHooks
}

func (r *commandRunner) Run(ctx context.Context, spec brain.RunSpec, callbacks claudecli.Callbacks) claudecli.Result {
	s, prompt := spec.Settings, spec.Prompt

	mcpConfig, settings := "", ""
	if s.Provider != "codex" && !s.InheritUser {
		mcpConfig = mcpConfigOf(spec.ID, spec.Dir)
		settings = excludeInstructionsAbove(spec.Dir)
	}
	args := claudeArgs(s, mcpConfig, settings)
	if s.Provider == "codex" {
		if err := claudecli.ValidateCodexTools(s.DisallowedTools); err != nil {
			return claudecli.Result{Err: err}
		}
		args = codexArgs(s, spec.Dir, r.sandbox != nil && r.sandbox.Enabled())
		prompt = strings.ReplaceAll(prompt, prompttext.AskUserQuestionNote, "If you need clarification, ask in your final reply and end the turn. The answer arrives as a new message.")
	}
	var providerEnv []string
	if s.Provider == "codex" && r.hooks != nil && r.hooks.CodexSetup != nil {
		extra, env, instructions, err := r.hooks.CodexSetup(spec.ID)
		if err != nil {
			return claudecli.Result{Err: err}
		}
		args = withArgsBeforePrint(args, extra)
		providerEnv = env
		prompt = instructions + prompt
	}
	if s.Provider != "codex" && r.hooks != nil && r.hooks.ExtraArgs != nil {
		extra := r.hooks.ExtraArgs(spec.ID)
		args = withArgsBeforePrint(args, extra)
	}

	// Wrap the spawn in sandbox-exec so prompt injection in an incoming message
	// cannot reach another brain's tree or cross-session Claude state. The brain
	// still runs with --dangerously-skip-permissions for tool freedom; the
	// sandbox enforces the read scope at the OS layer regardless. That is also
	// why a sandbox that cannot be set up refuses the run instead of spawning
	// without it: the fallback would quietly drop the only barrier left.
	binary := s.ClaudePath
	if s.Provider == "codex" {
		binary = s.CodexPath
	}
	agentBinary := binary
	finalArgs := args
	extraEnv := append([]string{"FRIDOLIN_BRAIN_ID=" + spec.ID}, dataEnv(s)...)
	extraEnv = append(extraEnv, providerEnv...)
	extraEnv = append(extraEnv, google.CalendarEnv(spec.ID, s.Manifest, s.CredsDir)...)
	extraEnv = append(extraEnv, plaudOptOut...)
	if s.ToolsDir != "" {
		// Prompts and skills run the helpers as $FRIDOLIN_TOOLS_DIR/<tool>,
		// wherever the bridge is installed.
		extraEnv = append(extraEnv, "FRIDOLIN_TOOLS_DIR="+s.ToolsDir)
	}
	if path := s.PlaudPATH; path != "" || len(s.OwnPATH) > 0 {
		if path == "" {
			path = os.Getenv("PATH")
		}
		dirs := append(slices.Clone(s.OwnPATH), filepath.SplitList(path)...)
		extraEnv = append(extraEnv, "PATH="+strings.Join(dirs, string(filepath.ListSeparator))) // replaces the bridge's
	}
	if r.hooks != nil && r.hooks.RunEnv != nil {
		env, done := r.hooks.RunEnv(spec.ID)
		// After Wait: the tools the run started have exited by then, or
		// are left behind and lose the brain API with the run.
		defer done()
		extraEnv = append(extraEnv, env...)
	}
	if s.Provider == "codex" || (r.sandbox != nil && r.sandbox.Enabled()) {
		// The brain's own Claude Code config dir, TMPDIR and caches, the
		// only places outside its folder and inbox the profile lets it
		// write, made before the profile pins them. With a config dir of
		// its own the CLI no longer finds your login in the keychain,
		// which the profile closes anyway: brains log in with
		// CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY from the bridge's
		// environment.
		stateEnv, err := prepareBrainState(s.DataDir, s.MediaDir, spec.ID, s.InheritUser)
		if err == nil && r.sandbox != nil && r.sandbox.Enabled() {
			binary, finalArgs, err = r.sandbox.Command(spec.ID, agentBinary, args)
		}
		if err != nil {
			log.Printf("brain %s: sandbox unavailable, refusing to run: %v", spec.ID, err)
			return claudecli.Result{Err: brain.ErrSandboxUnavailable}
		}
		extraEnv = append(extraEnv, stateEnv...)
		if s.Provider == "codex" {
			extraEnv = append(extraEnv, "CODEX_HOME="+brainStateOf(s.DataDir, spec.ID).codex)
		}
		// The profile denies writes to the CLI's own install, so its
		// auto-updater could download a new version on every run and never
		// switch to it. This replaces the bridge's own value.
		extraEnv = append(extraEnv, "DISABLE_AUTOUPDATER=1")
	}
	// Only the variables on childEnvAllowlist: anyone who may message the
	// brain can ask it to print its environment.
	scope := claudeChild
	if s.Provider == "codex" {
		scope = codexChild
	}
	env := childEnv(scope, s.EnvPassthrough, extraEnv...)

	// Only once the sandbox is settled: a refused run starts no process.
	spec.BeforeStart()

	started := callbacks.Started
	callbacks.Started = func(pid int) func() {
		cleanup := started(pid)
		rec := recordRun(s.DataDir, spec.ID, pid)
		return func() {
			if rec != "" {
				_ = os.Remove(rec)
			}
			if cleanup != nil {
				cleanup()
			}
		}
	}
	callbacks.GraceExpired = func() {
		log.Printf("brain %s: %s's output was still open %v after its result; stopping what is left of the run", spec.ID, claudecli.AgentName(s.Provider), postResultGrace)
	}
	return claudecli.Run(ctx, claudecli.Invocation{
		Run:      claudecli.Spawn{WaitDelay: runWaitDelay, Binary: binary, Args: finalArgs, Dir: spec.Dir, Env: env, Prompt: prompt},
		Provider: s.Provider, ExitGrace: postResultGrace, NoticeAfter: workingNoticeAfter,
		DeadlineError: claudecli.TimedOut(s.RunTimeout),
	}, callbacks)
}
