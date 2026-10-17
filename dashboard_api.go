package main

import (
	"context"
	"encoding/base64"
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

func (b *Bridge) apiMux() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/status", b.apiStatus)
	mux.HandleFunc("GET /api/whatsapp/qr", b.apiQR)
	mux.HandleFunc("POST /api/whatsapp/pair", b.apiPair)
	mux.HandleFunc("GET /api/whatsapp/groups", b.apiGroups)
	mux.HandleFunc("GET /api/activity", b.apiActivity)
	mux.HandleFunc("GET /api/runs", b.apiRuns)
	mux.HandleFunc("POST /api/runs/{brain}/stop", b.apiStopRun)
	mux.HandleFunc("GET /api/logs", b.apiLogs)

	mux.HandleFunc("GET /api/chats/ignored", b.apiIgnored)
	mux.HandleFunc("POST /api/chats/allow", b.apiAllowChat)
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
	mux.HandleFunc("POST /api/brains/migrate", b.apiMigrate)

	mux.HandleFunc("GET /api/fs/capabilities", b.apiFSCaps)
	mux.HandleFunc("POST /api/fs/pick", b.apiFSPick)
	mux.HandleFunc("GET /api/fs/list", b.apiFSList)

	mux.HandleFunc("GET /api/backup/status", b.apiBackupStatus)
	mux.HandleFunc("POST /api/backup/connect", b.apiBackupConnect)
	mux.HandleFunc("POST /api/backup/disconnect", b.apiBackupDisconnect)
	mux.HandleFunc("PUT /api/backup/settings", b.apiBackupSettings)
	mux.HandleFunc("POST /api/backup/run", b.apiBackupRun)
	mux.HandleFunc("POST /api/backup/cancel", b.apiBackupCancel)
	mux.HandleFunc("GET /api/backup/versions", b.apiBackupVersions)
	mux.HandleFunc("POST /api/backup/restore", b.apiBackupRestore)

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

	b.siteAPI(mux)

	mux.HandleFunc("POST /api/dev/inbound", b.apiDevInbound)
	mux.HandleFunc("GET /api/dev/outbox", b.apiDevOutbox)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "no such API endpoint")
	})
	return mux
}

func decodeJSON(r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return nil
	}
	dec := json.NewDecoder(r.Body)
	return dec.Decode(v)
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

var (
	claudeVersionMu    sync.Mutex
	claudeVersionCache = map[string]claudeStatus{}
)

// claudeInfo finds the CLI and its version, cached per path.
func claudeInfo(path string) claudeStatus {
	claudeVersionMu.Lock()
	defer claudeVersionMu.Unlock()
	if c, ok := claudeVersionCache[path]; ok {
		return c
	}
	st := claudeStatus{Path: path}
	if p, err := exec.LookPath(path); err == nil {
		st.Found, st.Path = true, p
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := exec.CommandContext(ctx, p, "--version").Output()
		cancel()
		if err == nil {
			st.Version = strings.TrimSpace(string(out))
		}
	}
	claudeVersionCache[path] = st
	return st
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
		Claude:     claudeInfo(cfg.ClaudePath),
		Sandbox:    sandboxStatus{Enabled: cfg.Sandbox.Enabled, Supported: runtime.GOOS == "darwin"},
		Issues:     b.brainMgr.Issues(),
		Warnings:   b.Warnings(),
		Backup:     b.backup.Summary(),
		Ingest:     cfg.Ingest.Enabled,
		Pending:    b.deb.Pending(),
	}
	if resp.Issues == nil {
		resp.Issues = []BrainIssue{}
	}
	if resp.Warnings == nil {
		resp.Warnings = []string{}
	}
	if cfg.AnyoneCanDM() {
		resp.Warnings = append(resp.Warnings, "allowed_numbers is empty, so anyone who can message this WhatsApp account can use the assistant. Add your number under Settings → Access.")
	}
	if cfg.DangerouslySkipPermissions && !cfg.Sandbox.Enabled && runtime.GOOS == "darwin" {
		resp.Warnings = append(resp.Warnings, "Brains run with all permissions and the sandbox is off. Turn the sandbox on under Settings → Sandbox.")
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
	if resp.Brains == nil {
		resp.Brains = []BrainRuntime{}
	}
	writeJSON(w, http.StatusOK, resp)
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
	png, err := qrcode.Encode(st.QRCode, qrcode.Medium, 320)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data_url":   "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
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
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	writeJSON(w, http.StatusOK, b.activity.Recent(limit, r.URL.Query().Get("brain")))
}

func (b *Bridge) apiRuns(w http.ResponseWriter, r *http.Request) {
	out := []BrainRuntime{}
	for _, br := range b.brainMgr.Brains() {
		out = append(out, br.Snapshot())
	}
	writeJSON(w, http.StatusOK, out)
}

func (b *Bridge) apiStopRun(w http.ResponseWriter, r *http.Request) {
	br := b.brainMgr.Get(r.PathValue("brain"))
	if br == nil {
		writeErr(w, http.StatusNotFound, "no such brain")
		return
	}
	var req struct {
		ClearQueue bool `json:"clear_queue"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"stopped": br.StopCurrent(req.ClearQueue)})
}

func (b *Bridge) apiLogs(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	lines, next := b.logs.Since(after, limit)
	if lines == nil {
		lines = []LogLine{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "next": next, "boot": b.started.Unix()})
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

// apiAllowChat allows a group or number and, for a group, assigns it to a
// brain — the dashboard version of copying a JID out of the log.
func (b *Bridge) apiAllowChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JID    string `json:"jid"`
		Kind   string `json:"kind"`   // group or dm
		Number string `json:"number"` // for dm
		Brain  string `json:"brain"`  // group: brain to route it to ("" = default brain)
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch req.Kind {
	case "group":
		if !strings.HasSuffix(req.JID, "@g.us") {
			writeErr(w, http.StatusBadRequest, "not a group JID")
			return
		}
		_, err := b.cfgs.Update("", func(c *Config) error {
			if !contains(c.AllowedGroups, req.JID) {
				c.AllowedGroups = append(c.AllowedGroups, req.JID)
			}
			if req.Brain == "" {
				return nil
			}
			return assignGroup(c, req.JID, req.Brain)
		})
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	case "dm":
		num := normalizeJID(req.Number)
		if num == "" {
			num = normalizeJID(req.JID)
		}
		if num == "" {
			writeErr(w, http.StatusBadRequest, "number required")
			return
		}
		if _, err := b.cfgs.Update("", func(c *Config) error {
			if !contains(c.AllowedNumbers, num) {
				c.AllowedNumbers = append(c.AllowedNumbers, num)
			}
			return nil
		}); err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "kind must be group or dm")
		return
	}
	_ = b.store.DismissIgnored(req.JID, true)
	writeJSON(w, http.StatusOK, map[string]string{"status": "allowed"})
}

// assignGroup routes a group to a brain, taking it away from any other brain.
// Folder brains get it in their manifest (brain.json and the approved copy);
// inline brains in their groups list.
func assignGroup(c *Config, jid, brainID string) error {
	found := false
	for i := range c.Brains {
		c.Brains[i].Groups = without(c.Brains[i].Groups, jid)
		if c.Brains[i].Name == brainID {
			c.Brains[i].Groups = append(c.Brains[i].Groups, jid)
			found = true
		}
	}
	for i := range c.BrainDirs {
		ref := &c.BrainDirs[i]
		if ref.Approved == nil {
			continue
		}
		m := *ref.Approved
		had := contains(m.Routing.Groups, jid)
		m.Routing.Groups = without(m.Routing.Groups, jid)
		if m.ID == brainID {
			m.Routing.Groups = append(m.Routing.Groups, jid)
			found = true
		} else if !had {
			continue
		}
		if err := WriteManifest(expandHomeDir(ref.Path), &m, true); err != nil {
			return err
		}
		ref.Approved = &m
	}
	if !found {
		return fmt.Errorf("no brain %q", brainID)
	}
	return nil
}

func (b *Bridge) apiDismissChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JID string `json:"jid"`
	}
	if err := decodeJSON(r, &req); err != nil || req.JID == "" {
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
	file.Brains, file.BrainDirs = nil, nil
	eff, _ := b.cfgs.Get().clone()
	eff.Brains, eff.BrainDirs = nil, nil
	writeJSON(w, http.StatusOK, map[string]any{
		"etag":      b.cfgs.ETag(),
		"path":      b.cfgs.Path(),
		"config":    file,
		"effective": eff,
	})
}

// apiPutConfig replaces the settings. Brains are managed through their own
// endpoints and are carried over untouched.
func (b *Bridge) apiPutConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ETag   string          `json:"etag"`
		Config json.RawMessage `json:"config"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var incoming Config
	if err := json.Unmarshal(req.Config, &incoming); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid config: "+err.Error())
		return
	}
	restart, err := b.cfgs.Update(req.ETag, func(c *Config) error {
		incoming.Brains, incoming.BrainDirs = c.Brains, c.BrainDirs
		*c = incoming
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
	if restart == nil {
		restart = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"etag": b.cfgs.ETag(), "restart_required": restart})
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
	Source   string         `json:"source"`
	Status   string         `json:"status"` // ok, drift, missing, invalid, unapproved
	Manifest *BrainManifest `json:"manifest"`
	Disk     *BrainManifest `json:"disk_manifest,omitempty"`
	Issues   []BrainIssue   `json:"issues"`
	Runtime  *BrainRuntime  `json:"runtime,omitempty"`
}

func (b *Bridge) brainViews() []brainView {
	cfg := b.cfgs.Get()
	_, issues := ResolveBrains(cfg)
	byPath := map[string][]BrainIssue{}
	// A brain left out because its folder would not open is known only to
	// the brain manager, until it loads the brains again.
	for _, is := range b.brainMgr.Issues() {
		if is.atLoad {
			issues = append(issues, is)
		}
	}
	for _, is := range issues {
		byPath[canonicalDir(is.Path)] = append(byPath[canonicalDir(is.Path)], is)
	}
	var out []brainView
	for _, bc := range cfg.Brains {
		m := ManifestFromInline(bc)
		v := brainView{ID: m.ID, Name: m.Name, Path: bc.WorkDir, Source: "inline", Status: "ok", Manifest: &m}
		out = append(out, v)
	}
	for _, ref := range cfg.BrainDirs {
		v := brainView{Path: ref.Path, Source: "folder", Status: "ok", Manifest: ref.Approved}
		if ref.Approved != nil {
			v.ID, v.Name = ref.Approved.ID, ref.Approved.Name
		}
		if disk, err := ReadManifest(canonicalDir(ref.Path)); err == nil && (ref.Approved == nil || !disk.Equal(*ref.Approved)) {
			v.Disk = disk
		}
		out = append(out, v)
	}
	for i := range out {
		out[i].Issues = byPath[canonicalDir(out[i].Path)]
		if out[i].Issues == nil {
			out[i].Issues = []BrainIssue{}
		}
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
	if out == nil {
		out = []brainView{}
	}
	return out
}

func (b *Bridge) apiBrains(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, b.brainViews())
}

func (b *Bridge) otherBrainDirs(exceptID string) []string {
	var dirs []string
	for _, v := range b.brainViews() {
		if v.ID != exceptID {
			dirs = append(dirs, expandHomeDir(v.Path))
		}
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
	abs, problems, warnings := CheckBrainPath(req.Path, b.cfgs.Get(), b.otherBrainDirs(""))
	resp := map[string]any{"abs_path": abs, "problems": nz(problems), "warnings": nz(warnings)}
	st, err := os.Stat(abs)
	resp["exists"] = err == nil
	resp["is_dir"] = err == nil && st.IsDir()
	if err == nil && st.IsDir() {
		entries, _ := os.ReadDir(abs)
		resp["entries"] = len(entries)
		_, gitErr := os.Stat(filepath.Join(abs, ".git"))
		resp["is_git"] = gitErr == nil
		_, promptErr := os.Stat(filepath.Join(abs, "brain-prompt.md"))
		resp["has_prompt"] = promptErr == nil
		_, histErr := os.Stat(filepath.Join(abs, "conversation-history.jsonl"))
		resp["has_history"] = histErr == nil
		if m, err := ReadManifest(abs); err == nil {
			resp["manifest"] = m
		}
	}
	id := slugify(filepath.Base(abs))
	taken := map[string]bool{}
	for _, v := range b.brainViews() {
		taken[v.ID] = true
	}
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", slugify(filepath.Base(abs)), i)
	}
	resp["suggested"] = map[string]string{"id": id, "name": prettyName(filepath.Base(abs))}
	writeJSON(w, http.StatusOK, resp)
}

// manifestHasBackup reports whether the request set any backup option.
func manifestHasBackup(m BrainManifest) bool {
	return m.Backup.Enabled || m.Backup.Exclude != nil
}

func prettyName(s string) string {
	s = strings.NewReplacer("-", " ", "_", " ").Replace(s)
	if s == "" {
		return "Brain"
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// apiAddBrain turns a folder into a brain: scaffold what is missing, write
// brain.json, register it, and start routing to it.
func (b *Bridge) apiAddBrain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path      string        `json:"path"`
		Manifest  BrainManifest `json:"manifest"`
		CreateDir bool          `json:"create_dir"`
		// UseExisting adopts a brain.json already in the folder (after the
		// dashboard showed it to you) instead of writing Manifest.
		UseExisting bool `json:"use_existing"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := b.cfgs.Get()
	abs, problems, _ := CheckBrainPath(req.Path, cfg, b.otherBrainDirs(""))
	if len(problems) > 0 {
		writeErr(w, http.StatusUnprocessableEntity, strings.Join(problems, "; "))
		return
	}
	if st, err := os.Stat(abs); err != nil {
		if !req.CreateDir {
			writeErr(w, http.StatusUnprocessableEntity, "folder does not exist")
			return
		}
	} else if !st.IsDir() {
		writeErr(w, http.StatusUnprocessableEntity, "not a folder")
		return
	}

	m := req.Manifest
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
	if !req.UseExisting && !manifestHasBackup(req.Manifest) {
		// No backup settings given: new brains are backed up, with the
		// default exclusions.
		m.Backup = BrainBackup{Enabled: true, Exclude: append([]string{}, DefaultBackupExcludes...)}
	} else if !req.UseExisting && m.Backup.Exclude == nil {
		m.Backup.Exclude = append([]string{}, DefaultBackupExcludes...)
	}
	m = m.normalized()
	if err := m.Validate(); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Check the new brain against the others before touching the disk.
	specs, _ := ResolveBrains(cfg)
	specs = append(specs, BrainSpec{Manifest: m, Dir: abs})
	if err := ValidateBrainSet(specs); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	created, err := ScaffoldBrain(abs, &m)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := b.cfgs.Update("", func(c *Config) error {
		approved := m
		c.BrainDirs = append(c.BrainDirs, BrainRef{Path: tildePath(abs), Approved: &approved})
		return nil
	}); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	log.Printf("brain %s: added (%s), created %v", m.ID, abs, created)
	writeJSON(w, http.StatusOK, map[string]any{"id": m.ID, "path": abs, "created": nz(created)})
}

// apiUpdateBrain saves edited settings for a brain. An inline brain is moved
// to its folder (brain.json) on its first edit.
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
		for i, bc := range c.Brains {
			if bc.Name == id {
				dir := expandHomeDir(bc.WorkDir)
				if err := WriteManifest(dir, &m, true); err != nil {
					return err
				}
				c.Brains = append(c.Brains[:i], c.Brains[i+1:]...)
				c.BrainDirs = append(c.BrainDirs, BrainRef{Path: bc.WorkDir, Approved: &m})
				return checkSet(c)
			}
		}
		for i := range c.BrainDirs {
			ref := &c.BrainDirs[i]
			if ref.Approved != nil && ref.Approved.ID == id {
				if err := WriteManifest(expandHomeDir(ref.Path), &m, true); err != nil {
					return err
				}
				ref.Approved = &m
				return checkSet(c)
			}
		}
		return fmt.Errorf("no brain %q", id)
	})
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
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

// apiApproveBrain accepts the brain.json currently on disk.
func (b *Bridge) apiApproveBrain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, err := b.cfgs.Update("", func(c *Config) error {
		for i := range c.BrainDirs {
			ref := &c.BrainDirs[i]
			if ref.Approved == nil || ref.Approved.ID != id {
				continue
			}
			disk, err := ReadManifest(expandHomeDir(ref.Path))
			if err != nil {
				return err
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
		return fmt.Errorf("no folder brain %q", id)
	})
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

// apiRemoveBrain stops routing to a brain. Its folder is left exactly as it is.
func (b *Bridge) apiRemoveBrain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, err := b.cfgs.Update("", func(c *Config) error {
		for i, bc := range c.Brains {
			if bc.Name == id {
				c.Brains = append(c.Brains[:i], c.Brains[i+1:]...)
				return nil
			}
		}
		path := r.URL.Query().Get("path")
		for i, ref := range c.BrainDirs {
			if (ref.Approved != nil && ref.Approved.ID == id) ||
				(ref.Approved == nil && path != "" && canonicalDir(expandHomeDir(ref.Path)) == canonicalDir(expandHomeDir(path))) {
				c.BrainDirs = append(c.BrainDirs[:i], c.BrainDirs[i+1:]...)
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

func (b *Bridge) apiMigrate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DryRun bool `json:"dry_run"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rep, err := MigrateInlineBrains(b.cfgs, req.DryRun, false)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
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
			return []string{p, "--getexistingdirectory", os.Getenv("HOME")}
		}
	}
	return nil
}

func (b *Bridge) apiFSCaps(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
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
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if err != nil {
		// Cancelling the dialog exits non-zero (osascript: error -128).
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
		return
	}
	p := strings.TrimRight(strings.TrimSpace(string(out)), "/")
	writeJSON(w, http.StatusOK, map[string]any{"path": p})
}

// apiFSList lists folders inside your home folder, for the in-dashboard
// folder browser. The bridge's data folder is hidden.
func (b *Bridge) apiFSList(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	home = canonicalDir(home)
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
		_, brainErr := os.Stat(filepath.Join(full, ManifestFile))
		_, gitErr := os.Stat(filepath.Join(full, ".git"))
		out = append(out, entry{Name: e.Name(), Path: full, IsBrain: brainErr == nil, IsGit: gitErr == nil})
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
	if b.dev == nil {
		writeErr(w, http.StatusNotFound, "only available with --no-whatsapp")
		return
	}
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
		writeErr(w, http.StatusBadRequest, "unknown brain "+strconvQuote(req.Brain))
		return
	}
	if req.Sender == "" {
		req.Sender = normalizeJID(req.JID)
	}
	// Apply the same access rules and trigger handling as WhatsApp messages.
	cfg := b.cfgs.Get()
	m := Inbound{JID: req.JID, Brain: req.Brain, Sender: req.Sender, Text: req.Text, Media: req.Media, Silent: req.Silent, Source: "dev", At: time.Now()}
	if m.IsGroup() {
		if !contains(cfg.AllowedGroups, m.JID) {
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
	if b.dev == nil {
		writeErr(w, http.StatusNotFound, "only available with --no-whatsapp")
		return
	}
	writeJSON(w, http.StatusOK, b.dev.Outbox())
}

// ── helpers ──────────────────────────────────────────────────────────────

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func without(list []string, s string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func expandHomeDir(p string) string {
	home, _ := os.UserHomeDir()
	return expandHome(p, home)
}

// tildePath writes paths under the home folder as ~/…, so config.json and
// backups stay valid on a machine with a different user name.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
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
