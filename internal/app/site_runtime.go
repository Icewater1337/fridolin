package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	sitesvc "github.com/Icewater1337/fridolin/internal/site"

	"github.com/Icewater1337/fridolin/internal/claudecli"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/sandbox"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// siteBuilderHooks connect the site to this bridge's brains and sandbox.
func (b *App) siteBuilderHooks() sitesvc.SiteBuilderHooks {
	return sitesvc.SiteBuilderHooks{
		BrainDir: func(id string) string {
			if br := b.brainMgr.Get(id); br != nil {
				return br.Dir()
			}
			return ""
		},
		Sandbox: func(brainID, bin string, args []string) (string, []string, []string, error) {
			sp := b.brainMgr.profiler
			if sp == nil || !sp.Enabled() {
				return bin, args, nil, nil
			}
			cfg := RuntimeConfig(b.cfgs)
			env, err := prepareBrainState(AbsPath(cfg.DataDir), cfg.MediaDir, brainID, cfg.InheritClaudeUserConfig)
			if err != nil {
				return "", nil, nil, err
			}
			sbBin, sbArgs, err := sp.Command(brainID, bin, args)
			return sbBin, sbArgs, env, err
		},
		BridgeURL: func() string { return "http://" + b.cfgs.Boot().HTTPListen },
		Executable: func() (string, error) {
			exe, err := os.Executable()
			if err != nil {
				return "", err
			}
			return filepath.EvalSymlinks(exe)
		},
		SiteLimits: func() sandbox.SiteLimits {
			var l sandbox.SiteLimits
			if sp := b.brainMgr.profiler; sp != nil {
				l.Deny, l.NoWrite = sp.sitePaths()
			}
			if cfg := RuntimeConfig(b.cfgs); cfg.HTTPEnabled {
				if _, port, err := net.SplitHostPort(cfg.HTTPListen); err == nil {
					l.BridgePort = port
				}
			}
			return l
		},
		MediaDir:    func() string { return b.cfgs.Boot().MediaDir },
		Search:      b.runBrainSearch,
		Device:      b.plaud.DeviceRequest,
		FilesBridge: b.filesBridge,
		// As /ingest: the brain's reply chat gets the answer, and the brain
		// must take the files now (its queue is not full).
		Ingest: func(brainID, sender, text string, files []string) error {
			cfg := RuntimeConfig(b.cfgs)
			if !cfg.Ingest.Enabled {
				return errors.New("ingest is turned off (ingest.enabled)")
			}
			br := b.brainMgr.Get(brainID)
			if br == nil {
				return fmt.Errorf("no brain %q", brainID)
			}
			jid := b.replyChatFor(br, cfg)
			if jid == "" {
				return fmt.Errorf("brain %s has no chat to reply in; set its reply chat", brainID)
			}
			return b.submitHandOff(br, jid, sender, text, files, nil)
		},
	}
}

// runBrainSearch is the bridge's search hook: a read-only Claude agent in
// the brain's folder, under the brain's sandbox, with the brain's login.
func (b *App) runBrainSearch(ctx context.Context, brainID, prompt string, out io.Writer) error {
	br := b.brainMgr.Get(brainID)
	if br == nil {
		return fmt.Errorf("no brain %q", brainID)
	}
	cfg := RuntimeConfig(b.cfgs)
	// Only the brain folder's own setup, as for a brain's runs, and tools
	// that only read.
	args := claudecli.ArgList(true, excludeInstructionsAbove(br.Dir(), cfg.Home), "--tools", "Read,Grep,Glob")
	bin, err := resolveAgentBinary("claude", cfg.ClaudePath)
	if err != nil {
		return err
	}
	env := childEnv(b.env, claudeChild, cfg.ChildEnvPassthrough)
	if sp := b.brainMgr.profiler; sp != nil && sp.Enabled() {
		// The brain's own Claude Code config dir, TMPDIR and caches, as for
		// its runs (see invoke): it may write nowhere else.
		state, err := prepareBrainState(AbsPath(cfg.DataDir), cfg.MediaDir, brainID, cfg.InheritClaudeUserConfig)
		if err != nil {
			return fmt.Errorf("the brain's sandbox: %w", err)
		}
		sbBin, sbArgs, err := sp.Command(brainID, bin, args)
		if err != nil {
			return fmt.Errorf("the brain's sandbox: %w", err)
		}
		bin, args = sbBin, sbArgs
		env = append(append(env, state...), "DISABLE_AUTOUPDATER=1")
	}
	proc, err := claudecli.Start(ctx, claudecli.Spawn{WaitDelay: runWaitDelay, Binary: bin, Args: args, Dir: br.Dir(), Env: env, Prompt: prompt, Stdout: out})
	if err == nil {
		err = proc.Wait()
	}
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	case proc == nil:
		return fmt.Errorf("claude: %w", err)
	}
	return fmt.Errorf("claude: %w: %s", err, textx.Tail(proc.StderrTail(), 400))
}

// siteChatMessenger sends what brains say in build chats to the Build page,
// everything else on as before.
type siteChatMessenger struct {
	Messenger
	appendMessage func(string, string, string) error
}

func (s siteChatMessenger) SendMessage(ctx context.Context, jid, text string) error {
	if id := brainOfBuildJID(jid); id != "" {
		return s.toChat(id, text)
	}
	return s.Messenger.SendMessage(ctx, jid, text)
}

func (s siteChatMessenger) SendChunked(ctx context.Context, jid, text string) error {
	if id := brainOfBuildJID(jid); id != "" {
		return s.toChat(id, text)
	}
	return s.Messenger.SendChunked(ctx, jid, text)
}

// TakesWhole reports that a build chat shows a reply in one piece, as
// SendChunked sends it: the Build page has no limit to split for.
func (s siteChatMessenger) TakesWhole(jid string) bool { return brainOfBuildJID(jid) != "" }

func (s siteChatMessenger) SendPoll(ctx context.Context, jid, q string, opts []string, max int, sending func(string)) (string, error) {
	if id := brainOfBuildJID(jid); id != "" {
		return "", s.toChat(id, q+"\n"+"• "+strings.Join(opts, "\n• "))
	}
	return s.Messenger.SendPoll(ctx, jid, q, opts, max, sending)
}

func (s siteChatMessenger) toChat(brainID, text string) error {
	// "Working on it" is what the page shows anyway while a run lasts. Only
	// that notice: others start with the same ⏳, and a build chat must not
	// miss that its message waits, or that Claude's usage limit is reached.
	if text == outboundmsg.WorkingNotice {
		return nil
	}
	return s.appendMessage(brainID, "assistant", text)
}
