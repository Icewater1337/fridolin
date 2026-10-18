package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"go.mau.fi/whatsmeow/types"
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
	mux.HandleFunc("POST /api/chats/dismiss", b.apiDismissChat)

	mux.HandleFunc("GET /api/config", b.apiGetConfig)
	mux.HandleFunc("PUT /api/config", b.apiPutConfig)
	mux.HandleFunc("POST /api/restart", b.apiRestart)

	mux.HandleFunc("GET /api/brains", b.apiBrains)
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

// errNoBody answers a request that left out the body its endpoint needs.
var errNoBody = errors.New("a JSON body is required")

// decodeJSON decodes the request's JSON body into v, strictly: a key the
// endpoint does not know and anything after the first value are errors that
// name what is wrong, rather than a typo silently dropped or a request that
// runs with Go's zero values. An empty body is errNoBody, sent chunked or
// not.
func decodeJSON(r *http.Request, v any) error { return decodeBody(r, v, false) }

// decodeOptionalJSON is decodeJSON for an endpoint whose body may be left
// out, such as stopping a run: an empty body leaves v as it is.
func decodeOptionalJSON(r *http.Request, v any) error { return decodeBody(r, v, true) }

// decodeStrict decodes data, one JSON value, as strictly as decodeJSON.
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func decodeBody(r *http.Request, v any, optional bool) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			if optional {
				return nil
			}
			return errNoBody
		}
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON: the body must be a single JSON value")
	}
	return nil
}

// ── status ───────────────────────────────────────────────────────────────

type statusResponse struct {
	Version    string         `json:"version"`
	StartedAt  time.Time      `json:"started_at"`
	UptimeS    int64          `json:"uptime_s"`
	ConfigPath string         `json:"config_path"`
	DataDir    string         `json:"data_dir"`
	DevMode    bool           `json:"dev_mode"`
	Platform   string         `json:"platform"`
	WhatsApp   waStatus       `json:"whatsapp"`
	Claude     claudeStatus   `json:"claude"`
	Sandbox    sandboxStatus  `json:"sandbox"`
	Brains     []BrainRuntime `json:"brains"`
	Issues     []BrainIssue   `json:"issues"`
	Warnings   []string       `json:"warnings"`
	Backup     backupSummary  `json:"backup"`
	Ingest     bool           `json:"ingest"`
	Pending    int            `json:"debouncing"`
	// Settings saved since the start that only apply after a restart. The
	// dashboard shows its restart banner from this, in every tab.
	RestartRequired []string `json:"restart_required"`
}

type waStatus struct {
	WAState
	QRAvailable bool `json:"qr_available"`
}

type claudeStatus struct {
	Path    string `json:"path"`
	Found   bool   `json:"found"`
	Version string `json:"version,omitempty"`
}

type sandboxStatus struct {
	Enabled   bool `json:"enabled"`
	Supported bool `json:"supported"`
}

func (b *Bridge) apiStatus(w http.ResponseWriter, r *http.Request) {
	cfg := b.cfgs.Get()
	wa := b.wa.State()
	resp := statusResponse{
		Version:    bridgeVersion(),
		StartedAt:  b.started,
		UptimeS:    int64(time.Since(b.started).Seconds()),
		ConfigPath: b.cfgs.Path(),
		DataDir:    cfg.DataDir,
		DevMode:    b.dev != nil,
		Platform:   runtime.GOOS,
		WhatsApp:   waStatus{WAState: wa, QRAvailable: wa.QRCode != ""},
		Claude:     claudeInfo(cfg.ClaudePath, cfg.ChildEnvPassthrough),
		Sandbox:    sandboxStatus{Enabled: cfg.Sandbox.Enabled, Supported: runtime.GOOS == "darwin"},
		Issues:     nonNil(b.brainIssues(cfg)),
		Warnings:   nonNil(b.Warnings()),
		Backup:     b.backup.Summary(),
		Ingest:     cfg.Ingest.Enabled,
		Pending:    b.deb.Pending(),

		RestartRequired: nonNil(b.cfgs.RestartRequired()),
	}
	if cfg.AnyoneCanDM() {
		resp.Warnings = append(resp.Warnings, "allowed_numbers is empty, so anyone who can message this WhatsApp account can use the assistant. Add your number under Chats → Allowed numbers.")
	}
	resp.Warnings = append(resp.Warnings, b.tokenWarnings(cfg, runtime.GOOS)...)
	if w := claudeVersionWarning(resp.Claude); w != "" {
		resp.Warnings = append(resp.Warnings, w)
	}
	if cfg.InheritClaudeUserConfig {
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
	}
	for _, br := range b.brainMgr.Brains() {
		resp.Brains = append(resp.Brains, br.Snapshot())
	}
	resp.Brains = nonNil(resp.Brains)
	writeJSON(w, http.StatusOK, resp)
}

// tokenWarnings says when the dashboard token does not keep brains out of
// the API: when it is off or about to be, and when brains with all
// permissions run without a sandbox, which on any OS lets them read the
// token (and the rest of the data dir) themselves. A sandbox that is on
// but cannot run gets its own warning: brains then refuse every message.
func (b *Bridge) tokenWarnings(cfg *Config, goos string) []string {
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
		writeErr(w, http.StatusNotFound, "no pairing code right now")
		return
	}
	// rsc.io/qr is what qrterminal draws the terminal's code with. Its PNG
	// is 8 pixels per module with the quiet zone included; the dashboard
	// scales it.
	code, err := qr.Encode(st.QRCode, qr.M)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data_url":   "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()),
		"created_at": st.QRAt,
	})
}

func (b *Bridge) apiPair(w http.ResponseWriter, r *http.Request) {
	if err := b.wa.StartPairing(); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "pairing"})
}

func (b *Bridge) apiGroups(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	groups, err := b.wa.JoinedGroups(ctx)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	cfg := b.cfgs.Get()
	type row struct {
		GroupInfo
		Allowed bool   `json:"allowed"`
		Brain   string `json:"brain"`
	}
	out := []row{}
	allowed := map[string]bool{}
	for _, g := range cfg.AllowedGroups {
		allowed[g] = true
	}
	for _, g := range groups {
		rw := row{GroupInfo: g, Allowed: allowed[g.JID]}
		if br := b.brainMgr.BrainFor(g.JID, true); br != nil && rw.Allowed {
			rw.Brain = br.ID()
		}
		out = append(out, rw)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	writeJSON(w, http.StatusOK, out)
}

func (b *Bridge) apiActivity(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)
	writeJSON(w, http.StatusOK, b.activity.Recent(limit, r.URL.Query().Get("brain")))
}

func (b *Bridge) apiStopRun(w http.ResponseWriter, r *http.Request) {
	br := b.brainMgr.Get(r.PathValue("id"))
	if br == nil {
		writeErr(w, http.StatusNotFound, "no such brain")
		return
	}
	var req struct {
		ClearQueue bool `json:"clear_queue"`
	}
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"stopped": br.StopCurrent(req.ClearQueue)})
}

func (b *Bridge) apiLogs(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 500
	}
	limit = min(limit, logRingLines)
	lines, next := b.logs.Since(after, limit)
	writeJSON(w, http.StatusOK, map[string]any{"lines": nonNil(lines), "next": next, "boot": b.started.Unix()})
}

// ── chats ────────────────────────────────────────────────────────────────

func (b *Bridge) apiIgnored(w http.ResponseWriter, r *http.Request) {
	chats, err := b.store.ListIgnored()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, chats)
}

// The forms allowed_numbers and allowed_groups hold. Every edit of the
// allow lists goes through allowListNumber and allowListGroup, so the
// dashboard's add boxes and its Allow buttons agree on what they accept.
var (
	phoneNumberRE = regexp.MustCompile(`^[0-9]{6,15}$`)
	groupJIDRE    = regexp.MustCompile(`^[0-9]+(-[0-9]+)?@g\.us$`)
)

// allowListNumber is s as allowed_numbers holds it: digits with the country
// code. It takes the ways people write a number (a +, spaces, dashes,
// dots, brackets, a WhatsApp JID) and refuses anything else, a @lid JID
// too: its digits are WhatsApp's internal id for the sender, not a number.
func allowListNumber(s string) (string, error) {
	if _, server, ok := strings.Cut(strings.TrimSpace(s), "@"); ok && server != types.DefaultUserServer {
		return "", fmt.Errorf("%q holds no phone number: send the number with the country code, e.g. 15551234567", strings.TrimSpace(s))
	}
	n := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\u00a0', '-', '.', '(', ')':
			return -1
		}
		return r
	}, normalizeJID(s))
	if !phoneNumberRE.MatchString(n) {
		return "", fmt.Errorf("%q is not a phone number: use digits with the country code, e.g. 15551234567", strings.TrimSpace(s))
	}
	return n, nil
}

// allowListGroup is s as allowed_groups holds it: a group JID.
func allowListGroup(s string) (string, error) {
	g := strings.TrimSpace(s)
	if !groupJIDRE.MatchString(g) {
		return "", fmt.Errorf("%q is not a group JID: it looks like 120363000000000000@g.us", g)
	}
	return g, nil
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
		entry, err = allowListGroup(req.sent())
		return false, entry, err
	case "dm":
		entry, err = allowListNumber(req.sent())
		return true, entry, err
	default:
		return false, "", errors.New("kind must be group or dm")
	}
}

// apiAllowChat allows a group or number and, for a group, assigns it to a
// brain — the dashboard version of copying a JID out of the log.
func (b *Bridge) apiAllowChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dm, entry, err := req.listEntry()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_, err = b.cfgs.Update("", func(c *Config) error {
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
		return assignGroup(c, entry, req.Brain)
	})
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// The chat no longer waits for access, also when it was typed in.
	waiting := entry
	if dm {
		waiting = req.JID
		if waiting == "" {
			waiting = userJID(entry)
		}
	}
	_ = b.store.DismissIgnored(waiting, true)
	writeJSON(w, http.StatusOK, map[string]string{"status": "allowed", "entry": entry})
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
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// An entry the bridge would refuse may still be on the list as it is
	// stored; only an unknown kind or no entry at all is refused at once.
	dm, entry, invalid := req.listEntry()
	if (req.Kind != "group" && req.Kind != "dm") || req.sent() == "" {
		writeErr(w, http.StatusBadRequest, invalid.Error())
		return
	}
	canon, name := allowListGroup, "allowed_groups"
	if dm {
		canon, name = allowListNumber, "allowed_numbers"
	}
	var removed []string
	_, err := b.cfgs.Update("", func(c *Config) error {
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
		return nil
	})
	switch {
	case errors.Is(err, errNotListed):
		writeErr(w, http.StatusNotFound, fmt.Sprintf("%s is not in %s", req.sent(), name))
	case err != nil:
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{"status": "revoked", "removed": removed})
	}
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
// in their manifests (brain.json and the approved copy). The chosen brain
// goes first, so that one that is not there is refused before the others'
// brain.json are written.
func assignGroup(c *Config, jid, brainID string) error {
	if err := updateManifestIn(c, brainID, func(m *BrainManifest) error {
		if !slices.Contains(m.Routing.Groups, jid) {
			m.Routing.Groups = append(m.Routing.Groups, jid)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, ref := range c.BrainDirs {
		if a := ref.Approved; a != nil && a.ID != brainID && slices.Contains(a.Routing.Groups, jid) {
			if err := updateManifestIn(c, a.ID, func(m *BrainManifest) error {
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
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.JID == "" {
		writeErr(w, http.StatusBadRequest, "jid required")
		return
	}
	if err := b.store.DismissIgnored(req.JID, false); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "dismissed"})
}

// ── config ───────────────────────────────────────────────────────────────

func (b *Bridge) apiGetConfig(w http.ResponseWriter, r *http.Request) {
	file := b.cfgs.FileCopy()
	file.BrainDirs = nil
	eff, err := b.cfgs.Get().clone()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	eff.BrainDirs = nil
	writeJSON(w, http.StatusOK, map[string]any{
		"etag":      b.cfgs.ETag(),
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
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ETag == "" {
		writeErr(w, http.StatusPreconditionRequired, "etag required: send the etag GET /api/config returned along with the config")
		return
	}
	incoming, err := decodeConfigReplacement(req.Config)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	restart, err := b.cfgs.Update(req.ETag, func(c *Config) error {
		incoming.BrainDirs = c.BrainDirs
		*c = *incoming
		return nil
	})
	switch {
	case errors.Is(err, ErrConfigConflict):
		writeErr(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"etag": b.cfgs.ETag(), "restart_required": nonNil(restart)})
}

// apiRestart re-executes the bridge in place (same binary, same arguments).
func (b *Bridge) apiRestart(w http.ResponseWriter, r *http.Request) {
	exe, err := os.Executable()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "restarting"})
	go func() {
		time.Sleep(300 * time.Millisecond)
		log.Printf("Restarting on request from the dashboard…")
		b.Stop() // releases the instance lock for the new process
		if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
			log.Fatalf("restart failed: %v", err)
		}
	}()
}

// ── brains ───────────────────────────────────────────────────────────────

type brainView struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Path     string         `json:"path"`
	Status   string         `json:"status"` // ok, drift, missing, invalid, unapproved
	Manifest *BrainManifest `json:"manifest"`
	Disk     *BrainManifest `json:"disk_manifest,omitempty"`
	Issues   []BrainIssue   `json:"issues"`
	Runtime  *BrainRuntime  `json:"runtime,omitempty"`
}

// brainIssues is what is wrong with the configured brains, the one list
// /api/status and /api/brains both show: the configuration read now, so a
// drift shows as soon as a brain.json changes, plus the folders the brain
// manager could not open when it last loaded the brains, which only it
// knows of.
func (b *Bridge) brainIssues(cfg *Config) []BrainIssue {
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
		if is.atLoad && (is.entry >= len(cfg.BrainDirs) || canonicalDir(cfg.BrainDirs[is.entry].Path) != canonicalDir(is.Path)) {
			continue
		}
		byEntry[is.entry] = append(byEntry[is.entry], is)
	}
	var out []brainView
	for _, ref := range cfg.BrainDirs {
		v := brainView{Path: ref.Path, Status: "ok", Manifest: ref.Approved}
		if ref.Approved != nil {
			v.ID, v.Name = ref.Approved.ID, ref.Approved.Name
		}
		if disk, err := ReadManifest(canonicalDir(ref.Path)); err == nil && (ref.Approved == nil || !disk.Equal(*ref.Approved)) {
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
		if br := b.brainMgr.Get(out[i].ID); br != nil && canonicalDir(br.Dir()) == canonicalDir(out[i].Path) {
			rt := br.Snapshot()
			out[i].Runtime = &rt
		}
	}
	return nonNil(out)
}

func (b *Bridge) apiBrains(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, b.brainViews())
}

// brainDirs lists every folder in c's brain_dirs, ~ expanded, approved or
// not: a new brain, or a restore, must not land in, around or on any of
// them, an entry still waiting for approval included.
func brainDirs(c *Config) []string {
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
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := b.cfgs.Get()
	abs, problems, warnings := CheckBrainPath(req.Path, cfg, brainDirs(cfg))
	resp := map[string]any{"abs_path": abs, "problems": nonNil(problems), "warnings": nonNil(warnings)}
	if len(problems) > 0 {
		// A folder that cannot be a brain is not looked into: outside the
		// home folder that would be a way to probe any path on disk.
		writeJSON(w, http.StatusOK, resp)
		return
	}
	st, err := os.Stat(abs)
	resp["exists"] = err == nil
	resp["is_dir"] = err == nil && st.IsDir()
	if err == nil && st.IsDir() {
		entries, _ := os.ReadDir(abs)
		resp["entries"] = len(entries)
		resp["is_git"] = fileExists(filepath.Join(abs, ".git"))
		resp["has_prompt"] = fileExists(filepath.Join(abs, defaultPromptFile))
		resp["has_history"] = fileExists(filepath.Join(abs, historyFileName))
		if m, err := ReadManifest(abs); err == nil {
			resp["manifest"] = m
		}
	}
	id := slugify(filepath.Base(abs))
	taken := map[string]bool{}
	for _, ref := range cfg.BrainDirs {
		if ref.Approved != nil {
			taken[ref.Approved.ID] = true
		}
	}
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", slugify(filepath.Base(abs)), i)
	}
	resp["suggested"] = map[string]string{"id": id, "name": prettyName(filepath.Base(abs))}
	writeJSON(w, http.StatusOK, resp)
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
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var m BrainManifest
	backupGiven := false
	if !req.UseExisting {
		if len(req.Manifest) == 0 {
			writeErr(w, http.StatusBadRequest, "manifest required (or use_existing, to add the brain.json in the folder)")
			return
		}
		if err := decodeStrict(req.Manifest, &m); err != nil {
			writeErr(w, http.StatusBadRequest, "manifest: "+err.Error())
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
		writeErr(w, http.StatusUnprocessableEntity, strings.Join(problems, "; "))
		return
	}
	// The first folder ScaffoldBrain will make, so a failed add can undo
	// them all: create_dir makes the parents too.
	made := firstMissing(abs)
	if st, err := os.Stat(abs); err != nil {
		if !req.CreateDir {
			writeErr(w, http.StatusUnprocessableEntity, "folder does not exist")
			return
		}
	} else if !st.IsDir() {
		writeErr(w, http.StatusUnprocessableEntity, "not a folder")
		return
	}

	if req.UseExisting {
		disk, err := ReadManifest(abs)
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, "no readable brain.json in that folder: "+err.Error())
			return
		}
		m = *disk
	} else if _, err := ReadManifest(abs); err == nil {
		writeErr(w, http.StatusConflict, "this folder already has a brain.json — review it and add it with “use existing”")
		return
	}
	switch {
	case req.UseExisting:
	case !backupGiven:
		// No backup settings given: new brains are backed up, with the
		// default exclusions.
		m.Backup = BrainBackup{Enabled: true, Exclude: append([]string{}, DefaultBackupExcludes...)}
	case m.Backup.Exclude == nil:
		m.Backup.Exclude = append([]string{}, DefaultBackupExcludes...)
	}
	m = m.normalized()
	if err := m.Validate(); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Check the new brain against the others before touching the disk.
	if err := checkNewBrain(cfg, m, abs); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	created, err := ScaffoldBrain(abs, &m)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := b.cfgs.Update("", func(c *Config) error {
		// Again, against the config this update changes. addMu keeps adds
		// apart, but not the other config writers: a brain saved or
		// approved since the check above may claim the same group or the
		// DMs, and the two must not both be registered.
		eff, err := effective(c)
		if err != nil {
			return err
		}
		if err := checkNewBrain(eff, m, abs); err != nil {
			return err
		}
		approved := m
		c.BrainDirs = append(c.BrainDirs, BrainRef{Path: tildePath(abs), Approved: &approved})
		return nil
	}); err != nil {
		unscaffold(abs, created, made)
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	log.Printf("brain %s: added (%s), created %v", m.ID, abs, created)
	writeJSON(w, http.StatusOK, map[string]any{"id": m.ID, "path": abs, "created": nonNil(created)})
}

// checkNewBrain checks brain m, to go in folder abs, against the brains cfg
// (an effective configuration) has: the folder must be none of theirs, in
// or around one, and m must not clash with them.
func checkNewBrain(cfg *Config, m BrainManifest, abs string) error {
	if _, problems, _ := CheckBrainPath(abs, cfg, brainDirs(cfg)); len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	specs, _ := ResolveBrains(cfg)
	return ValidateBrainSet(append(specs, BrainSpec{Manifest: m, Dir: abs}))
}

// firstMissing is the topmost folder of path that does not exist, the
// first one os.MkdirAll(path) makes; "" when path is there.
func firstMissing(path string) string {
	missing := ""
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			return missing
		}
		missing = p
		if filepath.Dir(p) == p {
			return missing
		}
	}
}

// unscaffold removes what ScaffoldBrain created in dir, the files in
// created, for a brain that was then not added, so the folder can be added
// again. Files that were there before stay. The folders it made for them
// go once they are empty, an empty one that was there before with them.
// made is the first folder that was missing (see firstMissing), or "":
// dir and the folders above it up to made go too, while they are empty.
func unscaffold(dir string, created []string, made string) {
	var dirs []string
	for _, rel := range created {
		if err := os.Remove(filepath.Join(dir, rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("removing %s after the brain was not added: %v", filepath.Join(dir, rel), err)
		}
		for d := filepath.Dir(rel); d != "." && !slices.Contains(dirs, d); d = filepath.Dir(d) {
			dirs = append(dirs, d)
		}
	}
	// Deepest first: a folder's path is longer than its parent's. Remove
	// refuses a folder that is not empty, which keeps one with files.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		_ = os.Remove(filepath.Join(dir, d))
	}
	if made == "" {
		return
	}
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if os.Remove(d) != nil || d == made {
			return
		}
	}
}

// apiUpdateBrain saves edited settings for a brain.
func (b *Bridge) apiUpdateBrain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Manifest BrainManifest `json:"manifest"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	m := req.Manifest.normalized()
	if m.ID != id {
		writeErr(w, http.StatusUnprocessableEntity, "a brain's id cannot change — remove it and add the folder again under a new id")
		return
	}
	if err := m.Validate(); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	_, err := b.cfgs.Update("", func(c *Config) error {
		// Check the brains with the edit before it is written: a refused
		// edit must leave brain.json as it was, not show up as drift.
		if err := checkSet(withApproved(c, id, m)); err != nil {
			return err
		}
		return updateManifestIn(c, id, func(cur *BrainManifest) error {
			*cur = m
			return nil
		})
	})
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// withApproved returns a copy of c in which brain id runs m, for checkSet.
func withApproved(c *Config, id string, m BrainManifest) *Config {
	probe := *c
	probe.BrainDirs = append([]BrainRef(nil), c.BrainDirs...)
	for i, ref := range probe.BrainDirs {
		if ref.Approved != nil && ref.Approved.ID == id {
			probe.BrainDirs[i].Approved = &m
		}
	}
	return &probe
}

// checkSet rejects an edit that would make two brains clash.
func checkSet(c *Config) error {
	eff, err := effective(c)
	if err != nil {
		return err
	}
	_, issues := ResolveBrains(eff)
	for _, is := range issues {
		if is.Kind == "invalid" {
			return errors.New(is.Detail)
		}
	}
	return nil
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
		Manifest *BrainManifest `json:"manifest"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Manifest == nil {
		writeErr(w, http.StatusBadRequest, "manifest required: send the brain.json you reviewed")
		return
	}
	_, err := b.cfgs.Update("", func(c *Config) error {
		for i := range c.BrainDirs {
			ref := &c.BrainDirs[i]
			if ref.Approved == nil || ref.Approved.ID != id {
				continue
			}
			disk, err := ReadManifest(ref.Dir())
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
			return checkSet(c)
		}
		return fmt.Errorf("no brain %q", id)
	})
	switch {
	case errors.Is(err, errReviewedManifestChanged):
		writeErr(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

// apiRemoveBrain stops routing to a brain. Its folder is left exactly as it is.
func (b *Bridge) apiRemoveBrain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, err := b.cfgs.Update("", func(c *Config) error {
		for i, ref := range c.BrainDirs {
			if ref.Approved != nil && ref.Approved.ID == id {
				c.BrainDirs = slices.Delete(c.BrainDirs, i, i+1)
				return nil
			}
		}
		return fmt.Errorf("no brain %q", id)
	})
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// apiRemoveBrainDir removes the brain_dirs entry for the folder ?path= that
// has no approved manifest. Such an entry has no id, so it cannot be
// removed as a brain; it comes from a hand-edited config.json. Its folder
// is left exactly as it is.
func (b *Bridge) apiRemoveBrainDir(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		writeErr(w, http.StatusBadRequest, "path required")
		return
	}
	want := canonicalDir(expandHome(path, homeDir()))
	_, err := b.cfgs.Update("", func(c *Config) error {
		for i, ref := range c.BrainDirs {
			if ref.Approved == nil && canonicalDir(ref.Dir()) == want {
				c.BrainDirs = slices.Delete(c.BrainDirs, i, i+1)
				return nil
			}
		}
		return fmt.Errorf("no unapproved brain folder %s (remove an approved brain by its id)", path)
	})
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// ── folder picking ───────────────────────────────────────────────────────

var pickMu sync.Mutex

// nativePicker returns the command that shows a native folder chooser.
func nativePicker() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"osascript", "-e", "activate", "-e", `POSIX path of (choose folder with prompt "Choose a folder for this brain")`}
	case "linux":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return nil
		}
		if p, err := exec.LookPath("zenity"); err == nil {
			return []string{p, "--file-selection", "--directory", "--title=Choose a folder for this brain"}
		}
		if p, err := exec.LookPath("kdialog"); err == nil {
			return []string{p, "--getexistingdirectory", homeDir()}
		}
	}
	return nil
}

func (b *Bridge) apiFSCaps(w http.ResponseWriter, r *http.Request) {
	home := homeDir()
	picker := ""
	if p := nativePicker(); p != nil {
		picker = filepath.Base(p[0])
	}
	writeJSON(w, http.StatusOK, map[string]string{"picker": picker, "home": home})
}

func (b *Bridge) apiFSPick(w http.ResponseWriter, r *http.Request) {
	argv := nativePicker()
	if argv == nil {
		writeErr(w, http.StatusNotImplemented, "no native folder picker on this system — browse or type the path instead")
		return
	}
	if !pickMu.TryLock() {
		writeErr(w, http.StatusConflict, "a folder picker is already open")
		return
	}
	defer pickMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	p, err := runPicker(ctx, argv)
	switch {
	case errors.Is(err, errPickerCancelled):
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
	case err != nil:
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("the folder dialog failed (%v) — browse or type the path instead", err))
	default:
		writeJSON(w, http.StatusOK, map[string]any{"path": p})
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
	home := canonicalDir(homeDir())
	p := r.URL.Query().Get("path")
	if p == "" {
		p = home
	}
	p = canonicalDir(expandHome(p, home))
	if !isWithin(p, home) {
		writeErr(w, http.StatusForbidden, "only folders inside your home folder can be browsed")
		return
	}
	showHidden := r.URL.Query().Get("hidden") == "1"
	entries, err := os.ReadDir(p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	data := canonicalDir(b.cfgs.Get().DataDir)
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
		if st, err := os.Stat(full); err != nil || !st.IsDir() {
			continue
		}
		if canonicalDir(full) == data {
			continue
		}
		out = append(out, entry{Name: e.Name(), Path: full, IsBrain: fileExists(filepath.Join(full, ManifestFile)), IsGit: fileExists(filepath.Join(full, ".git"))})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	parent := filepath.Dir(p)
	if p == home {
		parent = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "parent": parent, "home": home, "entries": out})
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
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.JID == "" {
		writeErr(w, http.StatusBadRequest, "jid required")
		return
	}
	req.Brain = strings.ToLower(strings.TrimSpace(req.Brain))
	if req.Brain != "" && b.brainMgr.Get(req.Brain) == nil {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("unknown brain %q", req.Brain))
		return
	}
	if req.Sender == "" {
		req.Sender = normalizeJID(req.JID)
	}
	// Apply the same access rules and trigger handling as WhatsApp messages.
	cfg := b.cfgs.Get()
	m := Inbound{JID: req.JID, Brain: req.Brain, Sender: req.Sender, Text: req.Text, Media: req.Media, Silent: req.Silent, Source: sourceDev, At: time.Now()}
	if m.IsGroup() {
		if !slices.Contains(cfg.AllowedGroups, m.JID) {
			b.recordIgnored("group", m.JID, "", m.Sender, "")
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored", "reason": "group not allowed"})
			return
		}
		if !cfg.Triggers.Matches(m.Text) && len(m.Media) == 0 {
			m.Silent = true
		}
		m.Text = cfg.Triggers.Strip(m.Text)
	} else if !numberAllowed(cfg, m.Sender) {
		b.recordIgnored("dm", m.JID, "", m.Sender, "")
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored", "reason": "number not allowed"})
		return
	}
	go b.handleInbound(context.Background(), m)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (b *Bridge) apiDevOutbox(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, b.dev.Outbox())
}

// ── helpers ──────────────────────────────────────────────────────────────

// tildePath writes paths under the home folder as ~/…, so config.json and
// backups stay valid on a machine with a different user name.
func tildePath(p string) string {
	home := homeDir()
	if home == "" {
		return p
	}
	home = canonicalDir(home)
	if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
		if rel == "." {
			return "~"
		}
		return "~/" + rel
	}
	return p
}
