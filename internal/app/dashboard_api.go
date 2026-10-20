package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/access"
	"github.com/Icewater1337/fridolin/internal/brain"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/registry"
	"github.com/Icewater1337/fridolin/internal/textx"
	"github.com/Icewater1337/fridolin/internal/version"
)

func (b *App) statusSnapshot(ctx context.Context) httpapi.StatusResponse {
	cfg := RuntimeConfig(b.cfgs)
	wa := b.wa.State()
	resp := httpapi.StatusResponse{
		Version:       version.String(),
		StartedAt:     b.started,
		UptimeS:       int64(time.Since(b.started).Seconds()),
		ConfigPath:    b.cfgs.Path(),
		DataDir:       cfg.DataDir,
		DevMode:       b.dev != nil,
		Platform:      runtime.GOOS,
		WhatsApp:      httpapi.WhatsAppStatus{LinkState: wa, QRAvailable: wa.QRCode != ""},
		Claude:        ClaudeInfo(cfg.ClaudePath, cfg.ChildEnvPassthrough),
		AgentProvider: string(cfg.AgentProvider),
		Sandbox:       httpapi.SandboxStatus{Enabled: cfg.Sandbox.Enabled, Supported: config.SandboxSupported(ConfigGOOS())},
		Issues:        nonNil(b.brainIssues(cfg)),
		Warnings:      nonNil(b.Warnings()),
		Backup:        b.backup.Summary(),
		Ingest:        cfg.Ingest.Enabled,
		Pending:       b.deb.Pending(),

		RestartRequired: nonNil(b.cfgs.RestartRequired()),
		Core:            b.coreStatus(),
	}
	if cfg.AgentProvider == "codex" {
		st := CodexInfo(cfg.CodexPath, cfg.ChildEnvPassthrough)
		resp.Codex = &st
		if !st.Found {
			resp.Warnings = append(resp.Warnings, "Codex CLI was not found. Set its path under Settings → AI mode.")
		}
		if os.Getenv("CODEX_API_KEY") == "" {
			for _, br := range b.brainMgr.Brains() {
				if !fsx.Exists(filepath.Join(brainStateOf(cfg.DataDir, br.ID()).codex, "auth.json")) {
					resp.Warnings = append(resp.Warnings, "Sign Codex in for brain "+br.ID()+" from Terminal: "+codexLoginHint(cfg.DataDir, br.ID()))
				}
			}
		}
	}
	resp.Warnings = append(resp.Warnings, b.coreWarnings()...)
	resp.Warnings = append(resp.Warnings, b.outboxWarnings(ctx)...)
	if bad := badAllowedNumbers(cfg.AllowedNumbers); len(bad) > 0 {
		resp.Warnings = append(resp.Warnings, fmt.Sprintf("allowed_numbers holds %q, which lets nobody in: each entry must be a phone number, digits with the country code. Fix it under Chats → Allowed numbers.", bad))
	}
	if cfg.AnyoneCanDM() {
		resp.Warnings = append(resp.Warnings, "allowed_numbers is empty, so anyone who can message this WhatsApp account can use the assistant. Add your number under Chats → Allowed numbers.")
	}
	resp.Warnings = append(resp.Warnings, b.tokenWarnings(b.cfgs.Get(), ConfigGOOS())...)
	if w := claudecli.VersionWarning(resp.Claude); cfg.AgentProvider != "codex" && w != "" {
		resp.Warnings = append(resp.Warnings, w)
	}
	if cfg.ScriptAPI.TokenlessSend {
		resp.Warnings = append(resp.Warnings, "/send works without a token (script_api.tokenless_send), so any program on this computer, brains included, can message your allowed chats. Have your scripts send the token in scripts.token in the data folder, then turn it off under Settings → Advanced.")
	}
	if cfg.AgentProvider != "codex" && cfg.InheritClaudeUserConfig {
		// Set in config.json only, so the dashboard shows it nowhere else.
		resp.Warnings = append(resp.Warnings, "Brains load your own Claude Code setup (inherit_claude_user_config in config.json): they run your hooks, read your instructions and can use your MCP servers for anyone who may message them.")
	}
	if cfg.Sandbox.Enabled {
		// The Sandbox card shows "On" from the config alone; say when that
		// does not hold.
		if err := b.brainMgr.profiler.Available(); err != nil {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("The sandbox is on but cannot run (%v), so brains refuse every message until it can.", err))
		} else if w := sandboxProfileWarning(b.brainMgr); w != "" {
			resp.Warnings = append(resp.Warnings, w)
		}
		if cfg.AgentProvider != "codex" && !brainLoginInEnv() {
			resp.Warnings = append(resp.Warnings, "Sandboxed brains cannot read your keychain, where the login you made with `claude` lives, and the bridge's environment has no CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY for them either: they fail with \"Not logged in\" unless their own settings log them in. Run `claude setup-token` and set the token in the bridge's environment (docs/CONFIGURATION.md, \"Where to set them\").")
		}
	}
	for _, br := range b.brainMgr.Brains() {
		resp.Brains = append(resp.Brains, br.Snapshot())
	}
	resp.Brains = nonNil(resp.Brains)
	return resp
}

// tokenWarnings says when the dashboard token does not keep brains out of
// the API: when it is off or about to be, and when brains with all
// permissions run without a sandbox, which on any OS lets them read the
// token (and the rest of the data dir) themselves. A sandbox that is on
// but cannot run gets its own warning: brains then refuse every message.
func (b *App) tokenWarnings(cfg *config.Settings, goos string) []string {
	var out []string
	switch {
	case !b.requiresToken():
		out = append(out, "The dashboard token is off (dashboard.require_token): any program on this computer, brains included, can change every setting, allow chats and turn the sandbox off. Turn it on under Settings → Advanced.")
	case !cfg.Dashboard.RequireToken:
		out = append(out, "The dashboard token was turned off (dashboard.require_token). That takes effect at the next restart: from then on any program on this computer, brains included, can change every setting. Turn it back on under Settings → Advanced to keep it.")
	}
	if cfg.DangerouslySkipPermissions && !cfg.Sandbox.Enabled {
		const risk = "so they can read and change the bridge's own data — the dashboard token, config.json and the WhatsApp session — and with them every setting."
		if config.SandboxSupported(goos) {
			out = append(out, "Brains run with all permissions and the sandbox is off, "+risk+" Turn the sandbox on under Settings → Sandbox.")
		} else {
			out = append(out, "Brains run with all permissions and there is no sandbox on "+goos+", "+risk+" Let only people you trust message the bridge.")
		}
	}
	return out
}

// outboxWaitWarning is how long a reply may wait before the Overview
// mentions it: a reconnect usually takes seconds.
const outboxWaitWarning = time.Minute

// outboxWarnings says what the outbox holds that the owner should know:
// replies that have waited a while, and replies given up on, which the
// chats never got.
func (b *App) outboxWarnings(ctx context.Context) []string {
	sum, err := b.store.OutboxSummary(ctx, time.Now())
	if err != nil {
		return []string{fmt.Sprintf("Cannot read the outbox of replies: %v", err)}
	}
	var out []string
	if sum.Waiting > 0 && time.Since(sum.OldestWaiting) > outboxWaitWarning {
		w := fmt.Sprintf("%d %s waiting to go out to WhatsApp, the oldest since %s; the bridge keeps trying for an hour.",
			sum.Waiting, plural(sum.Waiting, "reply is", "replies are"), sum.OldestWaiting.Local().Format("15:04"))
		if sum.WaitingError != "" {
			w += " Last error: " + textx.Truncate(sum.WaitingError, 200)
		}
		out = append(out, w)
	}
	if sum.Failed > 0 {
		out = append(out, fmt.Sprintf("%d %s could not be delivered to WhatsApp and %s given up on, the last at %s: %s. The chats never got them, and the brains' history does not hold them.",
			sum.Failed, plural(sum.Failed, "reply", "replies"), plural(sum.Failed, "was", "were"),
			sum.LastFailed.Local().Format("Jan 2 15:04"), textx.Truncate(sum.FailedError, 200)))
	}
	return out
}

// plural is one when n is 1, else many.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// brainLoginVars are the variables of the bridge's environment that log a
// brain's Claude CLI in, or point it at a provider that needs no login of
// Anthropic's: sandboxed brains get them (see childEnvAllowlist), but not
// your keychain login, since the sandbox closes the keychains to them.
var brainLoginVars = []string{
	"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
}

// brainLoginInEnv reports whether the bridge's environment holds one of
// brainLoginVars.
func brainLoginInEnv() bool {
	return slices.ContainsFunc(brainLoginVars, func(name string) bool { return os.Getenv(name) != "" })
}

// sandboxProfileWarning names the brains whose profile cannot be built, or
// returns "". Every profile names every brain's folder, so one path the
// profile cannot hold (a line break, a relative HOME) refuses the runs of
// every brain, and the Overview would only show each run's generic error.
func sandboxProfileWarning(bm *BrainManager) string {
	var failing []string
	var cause error
	for _, br := range bm.Brains() {
		if _, err := bm.profiler.Profile(br.ID()); err != nil {
			failing = append(failing, br.ID())
			if cause == nil {
				cause = err
			}
		}
	}
	switch len(failing) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("The sandbox profile cannot be built (%v), so brain %s refuses every message until that is fixed.", cause, failing[0])
	}
	return fmt.Sprintf("The sandbox profile cannot be built (%v), so brains %s refuse every message until that is fixed.", cause, strings.Join(failing, ", "))
}

// brainIssues is what is wrong with the configured brains, the one list
// /api/status and /api/brains both show: the configuration read now, so a
// drift shows as soon as a brain.json changes, plus the folders the brain
// manager could not open when it last loaded the brains, which only it
// knows of.
func (b *App) brainIssues(cfg *config.Settings) []registry.Issue {
	_, issues := ResolveBrains(cfg)
	for _, is := range b.brainMgr.Issues() {
		if is.AtLoad {
			issues = append(issues, is)
		}
	}
	return issues
}

func (b *App) brainViews() []httpapi.BrainView {
	cfg := b.cfgs.Get()
	issues := b.brainIssues(cfg)
	fallback := b.brainMgr.Fallback()
	base := registry.Views(cfg, issues)
	out := make([]httpapi.BrainView, len(base))
	for i, v := range base {
		out[i].View = v

		if br := b.brainMgr.Get(out[i].ID); br != nil && fsx.Canonical(br.Dir()) == fsx.Canonical(out[i].Path) {
			rt := br.Snapshot()
			out[i].Runtime = &rt
			out[i].Fallback = br == fallback
			out[i].Intents, _ = b.intents.List(br.ID())
		}
	}
	return nonNil(out)
}

var pickMu sync.Mutex

// errPickerCancelled is a folder dialog closed without a choice.
var errPickerCancelled = errors.New("no folder was chosen")

// runPicker runs the folder dialog argv and returns the folder chosen. A
// dialog that was cancelled, or outlived ctx, is errPickerCancelled; any
// other failure is an error of its own, so one that cannot open (osascript
// without automation access under launchd, say) does not pass for a
// cancel.
func runPicker(ctx context.Context, argv []string) (string, error) {
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if ctx.Err() != nil {
		return "", errPickerCancelled
	}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && pickerCancelled(filepath.Base(argv[0]), exit.ExitCode(), exit.Stderr):
		return "", errPickerCancelled
	case errors.As(err, &exit):
		if msg := strings.TrimSpace(string(exit.Stderr)); msg != "" {
			return "", fmt.Errorf("%w: %s", err, textTail(msg, 300))
		}
		return "", err
	case err != nil:
		return "", err
	}
	p := strings.TrimRight(strings.TrimSpace(string(out)), "/")
	if p == "" {
		return "", errPickerCancelled
	}
	return p, nil
}

// pickerCancelled reports whether a folder dialog that exited with code
// and stderr was cancelled: osascript says error -128, zenity and kdialog
// exit with 1.
func pickerCancelled(tool string, code int, stderr []byte) bool {
	switch tool {
	case "osascript":
		return code == 1 && bytes.Contains(stderr, []byte("-128"))
	case "zenity", "kdialog":
		return code == 1
	}
	return false
}

// textTail is the last n bytes of s, or s.
func textTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

func (b *App) submitDevInbound(req httpapi.DevInboundRequest) string {
	// Apply the same allow lists, group_members and trigger handling as
	// WhatsApp messages, on one config snapshot as handleGroupMessage does.
	cfg := b.cfgs.Get()
	m := brain.Inbound{Inbound: chat.Inbound{JID: req.JID, Brain: req.Brain, Sender: req.Sender, Text: req.Text, Media: req.Media, Silent: req.Silent, Source: chat.SourceDev, At: time.Now()}}
	if m.IsGroup() {
		if !isAllowedGroup(cfg, m.JID) {
			b.recordIgnored("group", m.JID, "", m.Sender, "")
			return "group not allowed"
		}
		// A member group_members does not admit is not read at all, and
		// the group, which is allowed, is not offered on the dashboard.
		if who := devGroupSender(m.Sender); !groupSenderAllowed(cfg, m.JID, who) {
			log.Printf("Dev group message in %s by %s: ignored (not admitted by group_members)", m.JID, who.masked())
			return "sender not admitted by group_members"
		}
		policy := AccessPolicy(cfg)
		policy.Files = b.groupRules(m.JID).files
		who := devGroupSender(m.Sender)
		decision := access.Decide(policy, chat.Envelope{Chat: m.JID, Sender: who.Number, SenderResolved: who.Resolved, Group: true, Text: m.Text, HasFile: len(m.Media) > 0})
		if decision.Silent {
			m.Silent = true
			// As on WhatsApp, a file that does not address the bridge is chatter.
			m.Media = nil
		}
		m.Text = decision.Text
	} else if !NumberAllowed(cfg, m.Sender) {
		b.recordIgnored("dm", m.JID, "", m.Sender, "")
		return "number not allowed"
	}
	go b.handleInbound(context.Background(), m)
	return ""
}

// devGroupSender is the member a dev message to a group comes from, as
// groupSenderAllowed sees a WhatsApp sender: their number when sender is
// one, written in any way phoneNumber takes. Anything else, a LID or the
// group's own digits when the request names no sender, is no number, like
// a member WhatsApp names only by a LID nothing maps to a number.
func devGroupSender(sender string) groupSender {
	if n, err := chat.PhoneNumber(sender); err == nil {
		return groupSender{Number: n, Resolved: true}
	}
	user, server, _ := strings.Cut(strings.TrimSpace(sender), "@")
	return groupSender{Number: chat.NormalizeJID(user), Server: server}
}

// nonNil returns s, or an empty slice for nil, so that the JSON says []
// rather than null, which the dashboard would have to check for.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func (b *App) apiMux() http.Handler {
	return httpapi.NewAPIMux(httpapi.APIDeps{
		Status:   httpapi.StatusAPI{Snapshot: b.statusSnapshot}.ServeHTTP,
		Activity: func(w http.ResponseWriter, r *http.Request) { httpapi.ActivityAPI{Reader: b.activity}.ServeHTTP(w, r) }, Logs: func(w http.ResponseWriter, r *http.Request) {
			httpapi.LogsAPI{Reader: b.logs, StartedAt: b.started, MaxLines: LogRingLines}.ServeHTTP(w, r)
		}, Restart: b.newRestartAPI().ServeHTTP,
		Site: b.siteAPI, DevMode: b.dev != nil,
		Connection: b.newConnectionAPI,
		Chats:      b.newChatsAPI,
		Config:     b.newConfigAPI,
		Brains:     b.newBrainsAPI,
		Filesystem: b.newFilesystemAPI,
		Backup:     b.newBackupAPI,
		Vault:      b.newVaultAPI,
		Google:     b.newGoogleAPI,
		Plaud:      b.newPlaudAPI,
		PlaudApp:   b.newPlaudAppAPI,
		Auth:       b.newDashboardAuth,
		Dev:        b.newDevAPI,
		Nodes:      b.newNodesAPI,
		Policy:     b.newPolicyAPI,
	})
}
