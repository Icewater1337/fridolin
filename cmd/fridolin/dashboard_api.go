package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/registry"
	"github.com/Icewater1337/fridolin/internal/route"
	"github.com/Icewater1337/fridolin/internal/textx"
	"github.com/Icewater1337/fridolin/internal/version"
	"rsc.io/qr"
)

func (b *Bridge) apiMux() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/status", b.apiStatus)
	mux.HandleFunc("GET /api/whatsapp/qr", b.apiQR)
	mux.HandleFunc("POST /api/whatsapp/pair", b.apiPair)
	mux.HandleFunc("GET /api/whatsapp/groups", b.apiGroups)
	mux.HandleFunc("GET /api/activity", b.apiActivity)
	mux.HandleFunc("GET /api/logs", b.apiLogs)

	mux.HandleFunc("GET /api/chats/ignored", b.apiIgnored)
	mux.HandleFunc("POST /api/chats/allow", b.apiAllowChat)
	mux.HandleFunc("POST /api/chats/revoke", b.apiRevokeChat)
	mux.HandleFunc("POST /api/chats/members", b.apiGroupMembers)
	mux.HandleFunc("POST /api/chats/dismiss", b.apiDismissChat)

	mux.HandleFunc("GET /api/config", b.apiGetConfig)
	mux.HandleFunc("PUT /api/config", b.apiPutConfig)
	mux.HandleFunc("POST /api/restart", b.apiRestart)

	mux.HandleFunc("POST /api/logout", b.apiLogout)
	mux.HandleFunc("POST /api/login/code", b.apiLoginCode)
	mux.HandleFunc("POST /api/token/rotate", b.apiRotateToken)

	mux.HandleFunc("GET /api/brains", b.apiBrains)
	mux.HandleFunc("GET /api/brains/{id}", b.apiBrain)
	mux.HandleFunc("POST /api/brains/inspect", b.apiInspectFolder)
	mux.HandleFunc("POST /api/brains", b.apiAddBrain)
	mux.HandleFunc("PUT /api/brains/{id}", b.apiUpdateBrain)
	mux.HandleFunc("POST /api/brains/{id}/approve", b.apiApproveBrain)
	mux.HandleFunc("DELETE /api/brains/{id}", b.apiRemoveBrain)
	mux.HandleFunc("POST /api/brains/{id}/stop", b.apiStopRun)
	mux.HandleFunc("DELETE /api/brain-dirs", b.apiRemoveBrainDir)

	mux.HandleFunc("GET /api/fs/capabilities", b.apiFSCaps)
	mux.HandleFunc("POST /api/fs/pick", b.apiFSPick)
	mux.HandleFunc("GET /api/fs/list", b.apiFSList)

	mux.HandleFunc("GET /api/backup/status", b.apiBackupStatus)
	mux.HandleFunc("POST /api/backup/connect", b.apiBackupConnect)
	mux.HandleFunc("POST /api/backup/disconnect", b.apiBackupDisconnect)
	mux.HandleFunc("PUT /api/backup/settings", b.apiBackupSettings)
	mux.HandleFunc("POST /api/backup/run", b.apiBackupRun)
	mux.HandleFunc("POST /api/backup/cancel", b.apiBackupCancel)
	mux.HandleFunc("POST /api/backup/restore", b.apiBackupRestore)
	mux.HandleFunc("PUT /api/brains/{id}/backup", b.apiBrainBackup)
	mux.HandleFunc("GET /api/brains/{id}/backup/versions", b.apiBackupVersions)

	mux.HandleFunc("GET /api/google/client", b.apiGoogleClient)
	mux.HandleFunc("PUT /api/google/client", b.apiSetGoogleClient)
	mux.HandleFunc("DELETE /api/google/client", b.apiDeleteGoogleClient)
	mux.HandleFunc("GET /api/brains/{id}/calendar", b.apiCalendarStatus)
	mux.HandleFunc("POST /api/brains/{id}/calendar/connect", b.apiCalendarConnect)
	mux.HandleFunc("PUT /api/brains/{id}/calendar", b.apiCalendarSelect)
	mux.HandleFunc("DELETE /api/brains/{id}/calendar", b.apiCalendarDisconnect)

	mux.HandleFunc("GET /api/plaud/status", b.apiPlaudStatus)
	mux.HandleFunc("POST /api/plaud/connect", b.apiPlaudConnect)
	mux.HandleFunc("POST /api/plaud/disconnect", b.apiPlaudDisconnect)
	mux.HandleFunc("PUT /api/plaud/settings", b.apiPlaudSettings)
	mux.HandleFunc("POST /api/plaud/sync", b.apiPlaudSync)
	mux.HandleFunc("POST /api/plaud/resend", b.apiPlaudResend)
	mux.HandleFunc("PUT /api/plaud/app/credentials", b.apiPlaudAppCredentials)
	mux.HandleFunc("POST /api/plaud/app/build", b.apiPlaudAppBuild)
	mux.HandleFunc("GET /api/plaud/app.apk", b.apiPlaudAppDownload)
	mux.HandleFunc("DELETE /api/plaud/devices/{id}", b.apiPlaudRevokeDevice)

	b.siteAPI(mux)

	// Only in dev mode: outside it they are not there at all (404).
	if b.dev != nil {
		mux.HandleFunc("POST /api/dev/inbound", b.apiDevInbound)
		mux.HandleFunc("GET /api/dev/outbox", b.apiDevOutbox)
	}

	// No catch-all: the mux answers an unknown path with 404 and a known
	// one asked with the wrong method with 405 and an Allow header.
	return mux
}

// writeJSONIfChanged writes v as writeJSON does, with an ETag of the body,
// and answers a request whose If-None-Match names that ETag with 304 and
// no body. The dashboard follows the activity feed and the brains every
// few seconds and sends back the ETag of what it has, so a poll that finds
// nothing new is not downloaded and compared again (app/resources.js).
func writeJSONIfChanged(w http.ResponseWriter, r *http.Request, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	body = append(body, '\n') // as writeJSON's encoder ends it
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:12]) + `"`
	w.Header().Set("ETag", etag)
	if etagListed(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// etagListed reports whether an If-None-Match header names etag. That
// header compares weakly (RFC 9110, 13.1.2), so a W/ in front is ignored.
func etagListed(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		if t = strings.TrimSpace(t); t == "*" || strings.TrimPrefix(t, "W/") == etag {
			return true
		}
	}
	return false
}

// ── status ───────────────────────────────────────────────────────────────

type statusResponse struct {
	Version       string            `json:"version"`
	StartedAt     time.Time         `json:"started_at"`
	UptimeS       int64             `json:"uptime_s"`
	ConfigPath    string            `json:"config_path"`
	DataDir       string            `json:"data_dir"`
	DevMode       bool              `json:"dev_mode"`
	Platform      string            `json:"platform"`
	WhatsApp      waStatus          `json:"whatsapp"`
	Claude        claudecli.Status  `json:"claude"`
	AgentProvider string            `json:"agent_provider"`
	Codex         *claudecli.Status `json:"codex,omitempty"`
	Sandbox       sandboxStatus     `json:"sandbox"`
	Brains        []BrainRuntime    `json:"brains"`
	Issues        []BrainIssue      `json:"issues"`
	Warnings      []string          `json:"warnings"`
	Backup        backupSummary     `json:"backup"`
	Ingest        bool              `json:"ingest"`
	Pending       int               `json:"debouncing"`
	// Settings saved since the start that only apply after a restart. The
	// dashboard shows its restart banner from this, in every tab.
	RestartRequired []string `json:"restart_required"`
}

type waStatus struct {
	chat.LinkState
	QRAvailable bool `json:"qr_available"`
}

type sandboxStatus struct {
	Enabled   bool `json:"enabled"`
	Supported bool `json:"supported"`
}

func (b *Bridge) apiStatus(w http.ResponseWriter, r *http.Request) {
	cfg := b.cfgs.Get()
	wa := b.wa.State()
	resp := statusResponse{
		Version:       version.String(),
		StartedAt:     b.started,
		UptimeS:       int64(time.Since(b.started).Seconds()),
		ConfigPath:    b.cfgs.Path(),
		DataDir:       cfg.DataDir,
		DevMode:       b.dev != nil,
		Platform:      runtime.GOOS,
		WhatsApp:      waStatus{LinkState: wa, QRAvailable: wa.QRCode != ""},
		Claude:        claudeInfo(cfg.ClaudePath, cfg.ChildEnvPassthrough),
		AgentProvider: cfg.AgentProvider,
		Sandbox:       sandboxStatus{Enabled: cfg.Sandbox.Enabled, Supported: runtime.GOOS == "darwin"},
		Issues:        nonNil(b.brainIssues(cfg)),
		Warnings:      nonNil(b.Warnings()),
		Backup:        b.backup.Summary(),
		Ingest:        cfg.Ingest.Enabled,
		Pending:       b.deb.Pending(),

		RestartRequired: nonNil(b.cfgs.RestartRequired()),
	}
	if cfg.AgentProvider == "codex" {
		st := codexInfo(cfg.CodexPath, cfg.ChildEnvPassthrough)
		resp.Codex = &st
		if !st.Found {
			resp.Warnings = append(resp.Warnings, "Codex CLI was not found. Set its path under Settings → AI mode.")
		}
		if os.Getenv("CODEX_API_KEY") == "" {
			for _, br := range b.brainMgr.Brains() {
				if !fsx.Exists(filepath.Join(brainStateOf(cfg.DataDir, br.id).codex, "auth.json")) {
					resp.Warnings = append(resp.Warnings, "Sign Codex in for brain "+br.id+" from Terminal: "+codexLoginHint(cfg.DataDir, br.id))
				}
			}
		}
	}
	resp.Warnings = append(resp.Warnings, b.outboxWarnings(r.Context())...)
	if bad := badAllowedNumbers(cfg.AllowedNumbers); len(bad) > 0 {
		resp.Warnings = append(resp.Warnings, fmt.Sprintf("allowed_numbers holds %q, which lets nobody in: each entry must be a phone number, digits with the country code. Fix it under Chats → Allowed numbers.", bad))
	}
	if cfg.AnyoneCanDM() {
		resp.Warnings = append(resp.Warnings, "allowed_numbers is empty, so anyone who can message this WhatsApp account can use the assistant. Add your number under Chats → Allowed numbers.")
	}
	resp.Warnings = append(resp.Warnings, b.tokenWarnings(cfg, runtime.GOOS)...)
	if w := claudecli.VersionWarning(resp.Claude); cfg.AgentProvider != "codex" && w != "" {
		resp.Warnings = append(resp.Warnings, w)
	}
	if cfg.TokenlessSend() {
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
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

// tokenWarnings says when the dashboard token does not keep brains out of
// the API: when it is off or about to be, and when brains with all
// permissions run without a sandbox, which on any OS lets them read the
// token (and the rest of the data dir) themselves. A sandbox that is on
// but cannot run gets its own warning: brains then refuse every message.
func (b *Bridge) tokenWarnings(cfg *config.Config, goos string) []string {
	var out []string
	switch {
	case !b.requiresToken():
		out = append(out, "The dashboard token is off (dashboard.require_token): any program on this computer, brains included, can change every setting, allow chats and turn the sandbox off. Turn it on under Settings → Advanced.")
	case !cfg.DashboardRequiresToken():
		out = append(out, "The dashboard token was turned off (dashboard.require_token). That takes effect at the next restart: from then on any program on this computer, brains included, can change every setting. Turn it back on under Settings → Advanced to keep it.")
	}
	if cfg.DangerouslySkipPermissions && !cfg.Sandbox.Enabled {
		const risk = "so they can read and change the bridge's own data — the dashboard token, config.json and the WhatsApp session — and with them every setting."
		if goos == "darwin" {
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
func (b *Bridge) outboxWarnings(ctx context.Context) []string {
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

func (b *Bridge) apiQR(w http.ResponseWriter, r *http.Request) {
	st := b.wa.State()
	if st.QRCode == "" {
		httpapi.WriteErr(w, http.StatusNotFound, "no pairing code right now")
		return
	}
	// rsc.io/qr is what qrterminal draws the terminal's code with. Its PNG
	// is 8 pixels per module with the quiet zone included; the dashboard
	// scales it.
	code, err := qr.Encode(st.QRCode, qr.M)
	if err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"data_url":   "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()),
		"created_at": st.QRAt,
	})
}

func (b *Bridge) apiPair(w http.ResponseWriter, r *http.Request) {
	if err := b.wa.StartPairing(); err != nil {
		httpapi.WriteErr(w, http.StatusConflict, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "pairing"})
}

// apiGroups answers GET /api/whatsapp/groups with the groups the account
// is in, from the list the WhatsApp client keeps. With ?fresh=1, which the
// Chats page's Refresh button sends, the client asks WhatsApp again first.
func (b *Bridge) apiGroups(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if r.URL.Query().Get("fresh") == "1" {
		if f, ok := b.wa.(interface{ ForgetGroups() }); ok {
			f.ForgetGroups()
		}
	}
	groups, err := b.wa.JoinedGroups(ctx)
	if err != nil {
		httpapi.WriteErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	cfg := b.cfgs.Get()
	type row struct {
		chat.Group
		Allowed bool   `json:"allowed"`
		Brain   string `json:"brain"`
	}
	out := []row{}
	allowed := map[string]bool{}
	for _, g := range cfg.AllowedGroups {
		allowed[g] = true
	}
	for _, g := range groups {
		rw := row{Group: g, Allowed: allowed[g.JID]}
		if br := b.brainMgr.BrainFor(g.JID, true); br != nil && rw.Allowed {
			rw.Brain = br.ID()
		}
		out = append(out, rw)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (b *Bridge) apiActivity(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)
	writeJSONIfChanged(w, r, b.activity.Recent(limit, r.URL.Query().Get("brain")))
}

func (b *Bridge) apiStopRun(w http.ResponseWriter, r *http.Request) {
	br := b.brainMgr.Get(r.PathValue("id"))
	if br == nil {
		httpapi.WriteErr(w, http.StatusNotFound, "no such brain")
		return
	}
	var req struct {
		ClearQueue bool `json:"clear_queue"`
	}
	if err := httpapi.DecodeOptionalJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]bool{"stopped": br.StopCurrent(req.ClearQueue)})
}

func (b *Bridge) apiLogs(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 500
	}
	limit = min(limit, logRingLines)
	lines, next := b.logs.Since(after, limit)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"lines": nonNil(lines), "next": next, "boot": b.started.Unix()})
}

// ── chats ────────────────────────────────────────────────────────────────

func (b *Bridge) apiIgnored(w http.ResponseWriter, r *http.Request) {
	chats, err := b.store.ListIgnored()
	if err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, chats)
}

// chatRequest is what /api/chats/allow and /api/chats/revoke take.
type chatRequest struct {
	JID    string `json:"jid"`
	Kind   string `json:"kind"`   // group or dm
	Number string `json:"number"` // for dm; the JID's number when empty
	Brain  string `json:"brain"`  // allow, group: brain to route it to ("" = default brain)
}

// sent is the entry as the request writes it, trimmed: the group JID, or
// for a dm the number (the JID when there is none).
func (req chatRequest) sent() string {
	if req.Kind == "dm" && strings.TrimSpace(req.Number) != "" {
		return strings.TrimSpace(req.Number)
	}
	return strings.TrimSpace(req.JID)
}

// listEntry checks the request and returns the allow list it is about and
// the entry, in the form the list holds.
func (req chatRequest) listEntry() (dm bool, entry string, err error) {
	switch req.Kind {
	case "group":
		entry, err = config.AllowListGroup(req.sent())
		return false, entry, err
	case "dm":
		entry, err = chat.PhoneNumber(req.sent())
		return true, entry, err
	default:
		return false, "", errors.New("kind must be group or dm")
	}
}

// apiAllowChat allows a group or number and, for a group, assigns it to a
// brain — the dashboard version of copying a JID out of the log.
func (b *Bridge) apiAllowChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dm, entry, err := req.listEntry()
	if err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_, err = b.cfgs.UpdateTx("", func(c *config.Config, tx *Tx) error {
		if dm {
			if !slices.Contains(c.AllowedNumbers, entry) {
				c.AllowedNumbers = append(c.AllowedNumbers, entry)
			}
			return nil
		}
		if !slices.Contains(c.AllowedGroups, entry) {
			c.AllowedGroups = append(c.AllowedGroups, entry)
		}
		if req.Brain == "" {
			return nil
		}
		return assignGroup(c, tx, entry, req.Brain)
	})
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// The chat no longer waits for access, also when it was typed in.
	waiting := entry
	if dm {
		waiting = req.JID
		if waiting == "" {
			waiting = chat.DMJID(entry)
		}
	}
	_ = b.store.DismissIgnored(waiting, true)
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "allowed", "entry": entry})
}

// errNotListed means the entry to revoke is not on its allow list.
var errNotListed = errors.New("not on the allow list")

// apiRevokeChat takes a number off allowed_numbers or a group off
// allowed_groups. A group keeps its place in a brain's routing, so allowing
// it again sends it to the same brain. Nothing on the list to remove is a
// 404, also for an entry the checks refuse, so the dashboard neither
// reports a removal that did not happen nor an error for one that
// happened elsewhere.
func (b *Bridge) apiRevokeChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// An entry the bridge would refuse may still be on the list as it is
	// stored; only an unknown kind or no entry at all is refused at once.
	dm, entry, invalid := req.listEntry()
	if (req.Kind != "group" && req.Kind != "dm") || req.sent() == "" {
		httpapi.WriteErr(w, http.StatusBadRequest, invalid.Error())
		return
	}
	canon, name := config.AllowListGroup, "allowed_groups"
	if dm {
		canon, name = chat.PhoneNumber, "allowed_numbers"
	}
	var removed []string
	_, err := b.cfgs.Update("", func(c *config.Config) error {
		list := &c.AllowedGroups
		if dm {
			list = &c.AllowedNumbers
		}
		var kept []string
		kept, removed = revokeEntry(*list, req.sent(), entry, invalid == nil, canon)
		if len(removed) == 0 {
			return errNotListed
		}
		*list = kept
		// A group no longer allowed takes its group_members entry along:
		// one left behind would be refused (validateGroupMembers).
		if !dm {
			for _, g := range removed {
				delete(c.GroupMembers, g)
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, errNotListed):
		httpapi.WriteErr(w, http.StatusNotFound, fmt.Sprintf("%s is not in %s", req.sent(), name))
	case err != nil:
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
	default:
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"status": "revoked", "removed": removed})
	}
}

// apiGroupMembers sets who in an allowed group reaches its brain, its
// group_members entry: ["*"] for every member, or the numbers given,
// stored as digits like allowed_numbers. members null, or left out,
// removes the entry, so that allowed_numbers decide again; an empty list
// lets nobody in. The list is checked as the config checks it
// (validateGroupMembers), and a group allowed_groups does not list is
// refused.
func (b *Bridge) apiGroupMembers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JID     string    `json:"jid"`
		Members *[]string `json:"members"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	group, err := config.AllowListGroup(req.JID)
	if err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var members []string
	if req.Members != nil {
		entry := map[string][]string{group: *req.Members}
		if err := config.ValidateGroupMembers(entry, []string{group}); err != nil {
			httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// A number typed twice, in two ways perhaps, is kept once.
		members = []string{}
		for _, n := range config.GroupMembersAsNumbers(entry)[group] {
			if !slices.Contains(members, n) {
				members = append(members, n)
			}
		}
	}
	_, err = b.cfgs.Update("", func(c *config.Config) error {
		if req.Members == nil {
			delete(c.GroupMembers, group)
			return nil
		}
		if c.GroupMembers == nil {
			c.GroupMembers = map[string][]string{}
		}
		c.GroupMembers[group] = members
		return nil
	})
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"status": "saved", "members": members})
}

// revokeEntry splits list into what stays and what revoking sent removes.
// The entry as it is stored goes first: the dashboard sends what it shows,
// which may be written by hand in a form the bridge does not take
// ("+15551234567", a JID with a space). Otherwise every entry that means
// entry (sent in the list's own form, when it is valid) goes.
func revokeEntry(list []string, sent, entry string, valid bool, canon func(string) (string, error)) (kept, removed []string) {
	exact := slices.ContainsFunc(list, func(x string) bool { return strings.TrimSpace(x) == sent })
	kept = list[:0:0]
	for _, x := range list {
		var match bool
		if exact {
			match = strings.TrimSpace(x) == sent
		} else if valid {
			c, err := canon(x)
			match = err == nil && c == entry
		}
		if match {
			removed = append(removed, x)
		} else {
			kept = append(kept, x)
		}
	}
	return kept, removed
}

// assignGroup routes a group to a brain, taking it away from any other brain,
// in their manifests (brain.json, staged in tx, and the approved copy). The
// chosen brain goes first, so that one that is not there is refused before
// the others are looked at.
func assignGroup(c *config.Config, tx *Tx, jid, brainID string) error {
	if err := updateManifestIn(c, tx, brainID, func(m *manifest.Manifest) error {
		if !slices.Contains(m.Routing.Groups, jid) {
			m.Routing.Groups = append(m.Routing.Groups, jid)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, ref := range c.BrainDirs {
		if a := ref.Approved; a != nil && a.ID != brainID && slices.Contains(a.Routing.Groups, jid) {
			if err := updateManifestIn(c, tx, a.ID, func(m *manifest.Manifest) error {
				m.Routing.Groups = slices.DeleteFunc(slices.Clone(m.Routing.Groups), func(g string) bool { return g == jid })
				return nil
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *Bridge) apiDismissChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JID string `json:"jid"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.JID == "" {
		httpapi.WriteErr(w, http.StatusBadRequest, "jid required")
		return
	}
	if err := b.store.DismissIgnored(req.JID, false); err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "dismissed"})
}

// ── config ───────────────────────────────────────────────────────────────

func (b *Bridge) apiGetConfig(w http.ResponseWriter, r *http.Request) {
	// One snapshot: the etag must be that of the config sent with it, or a
	// save that comes back with it would pass the conflict check over a
	// write the page never saw.
	snap := b.cfgs.Snapshot()
	file := mustClone(snap.File)
	file.BrainDirs = nil
	eff := mustClone(snap.Effective)
	eff.BrainDirs = nil
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"etag":      snap.ETag,
		"path":      b.cfgs.Path(),
		"config":    file,
		"effective": eff,
	})
}

// apiPutConfig replaces the settings. Brains are managed through their own
// endpoints and are carried over untouched.
//
// It replaces the whole file layer, so it takes only the whole of it, as
// GET /api/config returned it (see decodeConfigReplacement), and only with
// the etag it came with: a script that sends one setting, or a tab that
// saves over a newer change, would otherwise reset the rest to defaults —
// the sandbox off, nobody allowed, another data dir.
func (b *Bridge) apiPutConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ETag   string          `json:"etag"`
		Config json.RawMessage `json:"config"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ETag == "" {
		httpapi.WriteErr(w, http.StatusPreconditionRequired, "etag required: send the etag GET /api/config returned along with the config")
		return
	}
	incoming, err := decodeConfigReplacement(req.Config)
	if err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := b.cfgs.UpdateTx(req.ETag, func(c *config.Config, _ *Tx) error {
		incoming.BrainDirs = c.BrainDirs
		*c = *incoming
		return nil
	})
	switch {
	case errors.Is(err, ErrConfigConflict):
		httpapi.WriteErr(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// The etag of what this save wrote, not ETag(), which may be a later
	// save's: the page would take that write for the one it saved.
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"etag": res.ETag, "restart_required": nonNil(res.Restart)})
}

// apiRestart restarts the bridge in place (same binary, same arguments): it
// answers, then asks whoever runs the bridge to stop it and run it again
// (BridgeOptions.Restart). The stop is the one SIGTERM makes, in main, and
// main runs the bridge again once it is over.
func (b *Bridge) apiRestart(w http.ResponseWriter, r *http.Request) {
	if b.restart == nil {
		httpapi.WriteErr(w, http.StatusServiceUnavailable, "this bridge cannot restart itself")
		return
	}
	log.Printf("Restarting on request from the dashboard…")
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "restarting"})
	b.restart()
}

// ── brains ───────────────────────────────────────────────────────────────

type brainView struct {
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	Path     string             `json:"path"`
	Status   string             `json:"status"` // ok, drift, missing, invalid, unapproved
	Manifest *manifest.Manifest `json:"manifest"`
	Disk     *manifest.Manifest `json:"disk_manifest,omitempty"`
	Issues   []BrainIssue       `json:"issues"`
	Runtime  *BrainRuntime      `json:"runtime,omitempty"`
	// Fallback marks the brain that takes the chats no other brain
	// claims (BrainManager.Fallback). Without a default brain that is the
	// first one, which no brain.json says, so the dashboard names it.
	Fallback bool `json:"fallback"`
}

// brainIssues is what is wrong with the configured brains, the one list
// /api/status and /api/brains both show: the configuration read now, so a
// drift shows as soon as a brain.json changes, plus the folders the brain
// manager could not open when it last loaded the brains, which only it
// knows of.
func (b *Bridge) brainIssues(cfg *config.Config) []BrainIssue {
	_, issues := ResolveBrains(cfg)
	for _, is := range b.brainMgr.Issues() {
		if is.atLoad {
			issues = append(issues, is)
		}
	}
	return issues
}

func (b *Bridge) brainViews() []brainView {
	cfg := b.cfgs.Get()
	byEntry := map[int][]BrainIssue{}
	for _, is := range b.brainIssues(cfg) {
		// One found at load is about the entry it had then; skip it if the
		// entries changed since.
		if is.atLoad && (is.entry >= len(cfg.BrainDirs) || fsx.Canonical(cfg.BrainDirs[is.entry].Path) != fsx.Canonical(is.Path)) {
			continue
		}
		byEntry[is.entry] = append(byEntry[is.entry], is)
	}
	fallback := b.brainMgr.Fallback()
	var out []brainView
	for _, ref := range cfg.BrainDirs {
		v := brainView{Path: ref.Path, Status: "ok", Manifest: ref.Approved}
		if ref.Approved != nil {
			v.ID, v.Name = ref.Approved.ID, ref.Approved.Name
		}
		if disk, err := manifest.Read(fsx.Canonical(ref.Path)); err == nil && (ref.Approved == nil || !disk.Equal(*ref.Approved)) {
			v.Disk = disk
		}
		out = append(out, v)
	}
	for i := range out {
		out[i].Issues = nonNil(byEntry[i])
		for _, is := range out[i].Issues {
			if out[i].Status == "ok" || is.Kind != "drift" {
				out[i].Status = is.Kind
			}
		}
		if br := b.brainMgr.Get(out[i].ID); br != nil && fsx.Canonical(br.Dir()) == fsx.Canonical(out[i].Path) {
			rt := br.Snapshot()
			out[i].Runtime = &rt
			out[i].Fallback = br == fallback
		}
	}
	return nonNil(out)
}

func (b *Bridge) apiBrains(w http.ResponseWriter, r *http.Request) {
	writeJSONIfChanged(w, r, b.brainViews())
}

// apiBrain answers GET /api/brains/{id} with that brain's entry of
// /api/brains, so the Brain page can follow its brain without fetching
// every other one with it. Two folders whose brain.json claim one id are
// both listed there, and here the first is, as the page showed it when it
// picked its brain out of the list. It builds the whole list on purpose:
// reading only this brain's folder would be cheaper, but could tell the
// page something other than the list does (which folder wins, its issues),
// and saving the download is what matters.
func (b *Bridge) apiBrain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, v := range b.brainViews() {
		if v.ID == id {
			writeJSONIfChanged(w, r, v)
			return
		}
	}
	httpapi.WriteErr(w, http.StatusNotFound, "no such brain")
}

// brainDirs lists every folder in c's brain_dirs, ~ expanded, approved or
// not: a new brain, or a restore, must not land in, around or on any of
// them, an entry still waiting for approval included.
func brainDirs(c *config.Config) []string {
	var dirs []string
	for _, ref := range c.BrainDirs {
		dirs = append(dirs, ref.Dir())
	}
	return dirs
}

func (b *Bridge) apiInspectFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := b.cfgs.Get()
	abs, problems, warnings := CheckBrainPath(req.Path, cfg, brainDirs(cfg))
	resp := map[string]any{"abs_path": abs, "problems": nonNil(problems), "warnings": nonNil(warnings)}
	if len(problems) > 0 {
		// A folder that cannot be a brain is not looked into: outside the
		// home folder that would be a way to probe any path on disk.
		httpapi.WriteJSON(w, http.StatusOK, resp)
		return
	}
	st, err := os.Stat(abs)
	resp["exists"] = err == nil
	resp["is_dir"] = err == nil && st.IsDir()
	if err == nil && st.IsDir() {
		root, err := os.OpenRoot(fsx.Canonical(config.HomeDir()))
		if err != nil {
			httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
			return
		}
		defer root.Close()
		entries, _ := os.ReadDir(abs)
		resp["entries"] = len(entries)
		resp["is_git"] = fileExistsInRoot(root, filepath.Join(abs, ".git"))
		resp["has_prompt"] = fileExistsInRoot(root, filepath.Join(abs, manifest.DefaultPromptFile))
		resp["has_history"] = fileExistsInRoot(root, filepath.Join(abs, history.FileName))
		if m, err := manifest.Read(abs); err == nil {
			resp["manifest"] = m
		}
	}
	id := manifest.Slugify(filepath.Base(abs))
	taken := map[string]bool{}
	for _, ref := range cfg.BrainDirs {
		if ref.Approved != nil {
			taken[ref.Approved.ID] = true
		}
	}
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", manifest.Slugify(filepath.Base(abs)), i)
	}
	resp["suggested"] = map[string]string{"id": id, "name": prettyName(filepath.Base(abs))}
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

func prettyName(s string) string {
	s = strings.NewReplacer("-", " ", "_", " ").Replace(s)
	if s == "" {
		return "Brain"
	}
	// The first rune, not byte: a folder named "éclair" must not lose its é.
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// nonNil returns s, or an empty slice for nil, so that the JSON says []
// rather than null, which the dashboard would have to check for.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// apiAddBrain turns a folder into a brain: scaffold what is missing, write
// brain.json, register it, and start routing to it.
func (b *Bridge) apiAddBrain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		// Manifest is decoded below, where a backup setting left out must
		// be told from one set to false.
		Manifest  json.RawMessage `json:"manifest"`
		CreateDir bool            `json:"create_dir"`
		// UseExisting adopts a brain.json already in the folder (after the
		// dashboard showed it to you) instead of writing Manifest.
		UseExisting bool `json:"use_existing"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var m manifest.Manifest
	backupGiven := false
	if !req.UseExisting {
		if len(req.Manifest) == 0 {
			httpapi.WriteErr(w, http.StatusBadRequest, "manifest required (or use_existing, to add the brain.json in the folder)")
			return
		}
		if err := httpapi.DecodeStrict(req.Manifest, &m); err != nil {
			httpapi.WriteErr(w, http.StatusBadRequest, "manifest: "+err.Error())
			return
		}
		var keys map[string]json.RawMessage
		_ = json.Unmarshal(req.Manifest, &keys)
		backupGiven = keys["backup"] != nil && string(keys["backup"]) != "null"
	}
	// One add at a time, from the checks to the registration, so that an
	// add's checks see every brain the adds before it registered. Two adds
	// of the same new folder would otherwise each scaffold a part of it, and
	// the one that then lost the check in cfgs.Update would undo its part,
	// which can be the brain.json or prompt the other one was added with.
	b.addMu.Lock()
	defer b.addMu.Unlock()
	cfg := b.cfgs.Get()
	abs, problems, _ := CheckBrainPath(req.Path, cfg, brainDirs(cfg))
	if len(problems) > 0 {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, strings.Join(problems, "; "))
		return
	}
	abs, ok := inHome(abs)
	if !ok {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, "brain folders must be inside your home folder")
		return
	}
	if st, err := os.Stat(abs); err != nil {
		if !req.CreateDir {
			httpapi.WriteErr(w, http.StatusUnprocessableEntity, "folder does not exist")
			return
		}
	} else if !st.IsDir() {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, "not a folder")
		return
	}

	if req.UseExisting {
		disk, err := manifest.Read(abs)
		if err != nil {
			httpapi.WriteErr(w, http.StatusUnprocessableEntity, "no readable brain.json in that folder: "+err.Error())
			return
		}
		m = *disk
	} else if _, err := manifest.Read(abs); err == nil {
		httpapi.WriteErr(w, http.StatusConflict, "this folder already has a brain.json — review it and add it with “use existing”")
		return
	}
	switch {
	case req.UseExisting:
	case !backupGiven:
		// No backup settings given: new brains are backed up, with the
		// default exclusions.
		m.Backup = manifest.Backup{Enabled: true, Exclude: append([]string{}, manifest.DefaultBackupExcludes()...)}
	case m.Backup.Exclude == nil:
		m.Backup.Exclude = append([]string{}, manifest.DefaultBackupExcludes()...)
	}
	m = m.Normalized()
	if err := m.Validate(); err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Check the new brain against the others before touching the disk.
	if err := checkNewBrain(cfg, m, abs); err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Everything but brain.json, which goes with the update that registers
	// the brain: written only once the checks there pass, and taken away
	// again if config.json then cannot be written.
	scaffold, err := registry.ScaffoldFolder(abs, &m, brainTemplates)
	if err != nil {
		defer scaffold.Close()
		scaffold.Rollback()
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer scaffold.Close()
	created := scaffold.Created()
	writesManifest := false
	if _, err := b.cfgs.UpdateTx("", func(c *config.Config, tx *Tx) error {
		// Again, against the config this update changes. addMu keeps adds
		// apart, but not the other config writers: a brain saved or
		// approved since the check above may claim the same group or the
		// DMs, and the two must not both be registered.
		eff, err := config.Resolve(c)
		if err != nil {
			return err
		}
		if err := checkNewBrain(eff, m, abs); err != nil {
			return err
		}
		// A brain.json the folder has, one that does not load say, stays
		// as ScaffoldBrain would leave it.
		if _, err := fsx.ReadBrainFile(abs, manifest.File); !req.UseExisting && errors.Is(err, fs.ErrNotExist) {
			if err := tx.CreateManifest(abs, &m); err != nil {
				return err
			}
			writesManifest = true
		}
		approved := m
		c.BrainDirs = append(c.BrainDirs, config.BrainRef{Path: fsx.Tilde(abs, config.HomeDir()), Approved: &approved})
		return nil
	}); err != nil {
		scaffold.Rollback()
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if writesManifest {
		created = append(created, manifest.File)
		sort.Strings(created)
	}
	log.Printf("brain %s: added (%s), created %v", m.ID, abs, created)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"id": m.ID, "path": abs, "created": nonNil(created)})
}

// checkNewBrain checks brain m, to go in folder abs, against the brains cfg
// (an effective configuration) has: the folder must be none of theirs, in
// or around one, and m must not clash with them.
func checkNewBrain(cfg *config.Config, m manifest.Manifest, abs string) error {
	if _, problems, _ := CheckBrainPath(abs, cfg, brainDirs(cfg)); len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	specs, _ := ResolveBrains(cfg)
	return route.Validate(append(specs, manifest.Spec{Manifest: m, Dir: abs}))
}

// apiUpdateBrain saves edited settings for a brain.
func (b *Bridge) apiUpdateBrain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Manifest manifest.Manifest `json:"manifest"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	m := req.Manifest.Normalized()
	if m.ID != id {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, "a brain's id cannot change — remove it and add the folder again under a new id")
		return
	}
	if err := m.Validate(); err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// The brain.json the update stages has it check the brain set with
	// the edit before anything is written (see checkSet).
	_, err := b.cfgs.UpdateTx("", func(c *config.Config, tx *Tx) error {
		return updateManifestIn(c, tx, id, func(cur *manifest.Manifest) error {
			*cur = m
			return nil
		})
	})
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// checkSet refuses an edit that makes brains clash, or leaves one that
// cannot run: before and after are the effective configurations without
// and with the edit, and an "invalid" issue of after's brains that
// before's do not have refuses it. One that was there already, a brain a
// hand edit broke say, is not the edit's doing, and does not keep the
// other brains from being edited or approved. Issues are told apart by
// brain, kind and detail.
func checkSet(before, after *config.Config) error {
	_, was := ResolveBrains(before)
	seen := map[string]bool{}
	for _, is := range was {
		seen[issueKey(is)] = true
	}
	_, issues := ResolveBrains(after)
	for _, is := range issues {
		if is.Kind == "invalid" && !seen[issueKey(is)] {
			return errors.New(is.Detail)
		}
	}
	return nil
}

// issueKey tells brain issues apart for checkSet.
func issueKey(is BrainIssue) string {
	return is.ID + "\x00" + is.Kind + "\x00" + is.Detail
}

// errReviewedManifestChanged refuses an approval of a brain.json that is no
// longer the one on disk.
var errReviewedManifestChanged = errors.New("brain.json changed again since you reviewed it — look at the new changes and approve those")

// apiApproveBrain accepts the brain.json on disk, provided it is still the
// one the dashboard showed you: the request carries that manifest. A brain
// can rewrite its own brain.json at any moment, and approving whatever is
// on disk when the click arrives would grant it a change, routing say, that
// you never saw, without the confirmation such a change asks for.
func (b *Bridge) apiApproveBrain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Manifest *manifest.Manifest `json:"manifest"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Manifest == nil {
		httpapi.WriteErr(w, http.StatusBadRequest, "manifest required: send the brain.json you reviewed")
		return
	}
	_, err := b.cfgs.UpdateTx("", func(c *config.Config, tx *Tx) error {
		for i := range c.BrainDirs {
			ref := &c.BrainDirs[i]
			if ref.Approved == nil || ref.Approved.ID != id {
				continue
			}
			disk, err := manifest.Read(ref.Dir())
			if err != nil {
				return err
			}
			if !disk.Equal(*req.Manifest) {
				return errReviewedManifestChanged
			}
			if disk.ID != id {
				return fmt.Errorf("the brain.json on disk changed the id to %q — ids cannot change", disk.ID)
			}
			if err := disk.Validate(); err != nil {
				return err
			}
			ref.Approved = disk
			tx.CheckBrains()
			return nil
		}
		return fmt.Errorf("no brain %q", id)
	})
	switch {
	case errors.Is(err, errReviewedManifestChanged):
		httpapi.WriteErr(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

// apiRemoveBrain stops routing to a brain. Its folder is left exactly as it is.
func (b *Bridge) apiRemoveBrain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, err := b.cfgs.Update("", func(c *config.Config) error {
		for i, ref := range c.BrainDirs {
			if ref.Approved != nil && ref.Approved.ID == id {
				c.BrainDirs = slices.Delete(c.BrainDirs, i, i+1)
				return nil
			}
		}
		return fmt.Errorf("no brain %q", id)
	})
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// apiRemoveBrainDir removes the brain_dirs entry for the folder ?path= that
// has no approved manifest. Such an entry has no id, so it cannot be
// removed as a brain; it comes from a hand-edited config.json. Its folder
// is left exactly as it is.
func (b *Bridge) apiRemoveBrainDir(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		httpapi.WriteErr(w, http.StatusBadRequest, "path required")
		return
	}
	want := fsx.Canonical(fsx.ExpandHome(path, config.HomeDir()))
	_, err := b.cfgs.Update("", func(c *config.Config) error {
		for i, ref := range c.BrainDirs {
			if ref.Approved == nil && fsx.Canonical(ref.Dir()) == want {
				c.BrainDirs = slices.Delete(c.BrainDirs, i, i+1)
				return nil
			}
		}
		return fmt.Errorf("no unapproved brain folder %s (remove an approved brain by its id)", path)
	})
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// ── folder picking ───────────────────────────────────────────────────────

var pickMu sync.Mutex

func (b *Bridge) apiFSCaps(w http.ResponseWriter, r *http.Request) {
	home := config.HomeDir()
	picker := ""
	if p := platform.FolderPicker(home); p != nil {
		picker = filepath.Base(p[0])
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"picker": picker, "home": home})
}

func (b *Bridge) apiFSPick(w http.ResponseWriter, r *http.Request) {
	argv := platform.FolderPicker(config.HomeDir())
	if argv == nil {
		httpapi.WriteErr(w, http.StatusNotImplemented, "no native folder picker on this system — browse or type the path instead")
		return
	}
	if !pickMu.TryLock() {
		httpapi.WriteErr(w, http.StatusConflict, "a folder picker is already open")
		return
	}
	defer pickMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	p, err := runPicker(ctx, argv)
	switch {
	case errors.Is(err, errPickerCancelled):
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"cancelled": true})
	case err != nil:
		httpapi.WriteErr(w, http.StatusInternalServerError, fmt.Sprintf("the folder dialog failed (%v) — browse or type the path instead", err))
	default:
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"path": p})
	}
}

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

// apiFSList lists folders inside your home folder, for the in-dashboard
// folder browser. The bridge's data folder is hidden.
func (b *Bridge) apiFSList(w http.ResponseWriter, r *http.Request) {
	home := fsx.Canonical(config.HomeDir())
	p := r.URL.Query().Get("path")
	if p == "" {
		p = home
	}
	p = fsx.Canonical(fsx.ExpandHome(p, home))
	if !fsx.Within(p, home) {
		httpapi.WriteErr(w, http.StatusForbidden, "only folders inside your home folder can be browsed")
		return
	}
	showHidden := r.URL.Query().Get("hidden") == "1"
	root, err := os.OpenRoot(home)
	if err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	defer root.Close()
	rel, err := filepath.Rel(home, p)
	if err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dir, err := root.Open(rel)
	if err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	data := fsx.Canonical(b.cfgs.Get().DataDir)
	type entry struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		IsBrain bool   `json:"is_brain"`
		IsGit   bool   `json:"is_git"`
	}
	out := []entry{}
	for _, e := range entries {
		if !e.IsDir() && e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") && !showHidden {
			continue
		}
		full := filepath.Join(p, e.Name())
		// Checking the parent is not enough: a child may be a symlink out
		// of home. Resolve it before probing its type or marker files, then
		// use the home root so a subsequent symlink swap cannot escape.
		resolved := fsx.Canonical(full)
		if !fsx.Within(resolved, home) || resolved == data {
			continue
		}
		child, err := filepath.Rel(home, resolved)
		if err != nil {
			continue
		}
		if st, err := root.Stat(child); err != nil || !st.IsDir() {
			continue
		}
		out = append(out, entry{Name: e.Name(), Path: full, IsBrain: fileExistsInRoot(root, filepath.Join(resolved, manifest.File)), IsGit: fileExistsInRoot(root, filepath.Join(resolved, ".git"))})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	parent := filepath.Dir(p)
	if p == home {
		parent = ""
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"path": p, "parent": parent, "home": home, "entries": out})
}

// fileExistsInRoot probes metadata only inside root. Resolve first so an
// absolute symlink to another allowed location works too; Root.Stat keeps
// a later symlink replacement from redirecting the probe outside the root.
func fileExistsInRoot(root *os.Root, p string) bool {
	rel, err := filepath.Rel(root.Name(), fsx.Canonical(p))
	if err != nil || (rel != "." && !filepath.IsLocal(rel)) {
		return false
	}
	_, err = root.Stat(rel)
	return err == nil
}

// ── dev mode ─────────────────────────────────────────────────────────────

func (b *Bridge) apiDevInbound(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JID    string   `json:"jid"`
		Sender string   `json:"sender"`
		Text   string   `json:"text"`
		Media  []string `json:"media"`
		Silent bool     `json:"silent"`
		// Brain runs the message in that brain instead of the chat's own,
		// the way /ingest and Plaud hand files to a brain.
		Brain string `json:"brain"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.JID == "" {
		httpapi.WriteErr(w, http.StatusBadRequest, "jid required")
		return
	}
	req.Brain = strings.ToLower(strings.TrimSpace(req.Brain))
	if req.Brain != "" && b.brainMgr.Get(req.Brain) == nil {
		httpapi.WriteErr(w, http.StatusBadRequest, fmt.Sprintf("unknown brain %q", req.Brain))
		return
	}
	if req.Sender == "" {
		req.Sender = chat.NormalizeJID(req.JID)
	}
	// Apply the same allow lists, group_members and trigger handling as
	// WhatsApp messages, on one config snapshot as handleGroupMessage does.
	cfg := b.cfgs.Get()
	m := Inbound{Inbound: chat.Inbound{JID: req.JID, Brain: req.Brain, Sender: req.Sender, Text: req.Text, Media: req.Media, Silent: req.Silent, Source: chat.SourceDev, At: time.Now()}}
	if m.IsGroup() {
		if !isAllowedGroup(cfg, m.JID) {
			b.recordIgnored("group", m.JID, "", m.Sender, "")
			httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "ignored", "reason": "group not allowed"})
			return
		}
		// A member group_members does not admit is not read at all, and
		// the group, which is allowed, is not offered on the dashboard.
		if who := devGroupSender(m.Sender); !groupSenderAllowed(cfg, m.JID, who) {
			log.Printf("Dev group message in %s by %s: ignored (not admitted by group_members)", m.JID, who.masked())
			httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "ignored", "reason": "sender not admitted by group_members"})
			return
		}
		files := len(m.Media) > 0 && b.groupRules(m.JID).files
		if !cfg.Triggers.Matches(m.Text) && !files {
			m.Silent = true
			// As on WhatsApp, a file that does not address the bridge is
			// chatter, and not fetched.
			m.Media = nil
		}
		m.Text = cfg.Triggers.Strip(m.Text)
	} else if !numberAllowed(cfg, m.Sender) {
		b.recordIgnored("dm", m.JID, "", m.Sender, "")
		httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "ignored", "reason": "number not allowed"})
		return
	}
	go b.handleInbound(context.Background(), m)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
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

func (b *Bridge) apiDevOutbox(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, b.dev.Outbox())
}

// ── helpers ──────────────────────────────────────────────────────────────
