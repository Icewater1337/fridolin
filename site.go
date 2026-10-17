package main

// The public website (docs/SITE.md): its settings, accounts, repository and
// processes.
//
// Fridolin owns all of it. The site process (site/, Node) runs separately and
// only reads what this writes into <data_dir>/site: runtime.json, users.json
// and session.key. It never sees the bridge's dashboard token, its config or
// its environment. Which code runs is decided here too: the site serves one
// approved commit of the site repository, deployed into its own folder.

import (
	"bufio"
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	defaultSiteListen = "127.0.0.1:8780"
	sitePBKDF2Iter    = 600_000
	siteKeepDeploys   = 3
)

// SiteSettings is <data_dir>/site/settings.json, edited from the dashboard.
type SiteSettings struct {
	Enabled bool   `json:"enabled"`
	Title   string `json:"title"`
	// Listen is where the site process listens; a tunnel forwards the domain to it.
	Listen string `json:"listen"`
	// Domain is the public host name, e.g. "example.com" (https assumed).
	Domain string   `json:"domain"`
	Repo   SiteRepo `json:"repo"`
	// Roles users can have. "admin" is always one of them.
	Roles []string `json:"roles"`
	// Approved is the commit the live site serves.
	Approved string       `json:"approved"`
	History  []SiteDeploy `json:"history"`
	// Tunnel runs cloudflared with the saved tunnel token.
	Tunnel bool `json:"tunnel"`
	// Builders are the brains allowed to build the site (site_builder.go).
	Builders  []string       `json:"builders"`
	Proposals []SiteProposal `json:"proposals"`
}

// SiteRepo is the private repository that holds the site's modules.
type SiteRepo struct {
	URL    string `json:"url"`
	Branch string `json:"branch"`
	// Subdir is the folder inside the repository that holds modules/ ("" = the root).
	Subdir string `json:"subdir"`
}

// SiteDeploy is one approved commit.
type SiteDeploy struct {
	Commit  string `json:"commit"`
	Subject string `json:"subject"`
	At      string `json:"at"`
}

// SiteUser is one account in users.json. The site process reads the same file.
type SiteUser struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"` // google | password
	Email        string `json:"email,omitempty"`
	Username     string `json:"username,omitempty"`
	Name         string `json:"name,omitempty"`
	Role         string `json:"role"`
	PasswordHash string `json:"password_hash,omitempty"`
	Created      string `json:"created"`
}

// SiteManager runs the site.
type SiteManager struct {
	dataDir      string
	googleClient func() string // path of Fridolin's shared Google OAuth client file

	// Swappable for tests: installing npm dependencies into a folder, and
	// into a builder brain's workspace (as that brain).
	npmInstall       func(ctx context.Context, dir string, prod bool) error
	workspaceInstall func(ctx context.Context, brainID, dir string) error

	mu       sync.Mutex
	settings SiteSettings
	users    []SiteUser
	site     *siteProc
	tunnel   *siteProc
	deploy   *siteJob
	previews map[string]*sitePreview
	tokens   map[string]string // builder token → brain id
	hooks    builderHooks
	ctx      context.Context
}

// NewSiteManager loads the site's settings and accounts from dataDir/site.
func NewSiteManager(dataDir string, googleClient func() string) *SiteManager {
	m := &SiteManager{
		dataDir:      dataDir,
		googleClient: googleClient,
		previews:     map[string]*sitePreview{},
		site:         newSiteProc("site"),
		tunnel:       newSiteProc("tunnel"),
	}
	m.npmInstall = m.runNpmInstall
	m.workspaceInstall = m.npmInstallAsBrain
	m.settings = m.loadSettings()
	m.users = m.loadUsers()
	return m
}

func (m *SiteManager) dir(parts ...string) string {
	return filepath.Join(append([]string{m.dataDir, "site"}, parts...)...)
}

func (m *SiteManager) loadSettings() SiteSettings {
	var s SiteSettings
	if data, err := os.ReadFile(m.dir("settings.json")); err == nil {
		if err := json.Unmarshal(data, &s); err != nil {
			log.Printf("[site] %s is not valid JSON, using defaults: %v", m.dir("settings.json"), err)
		}
	}
	if s.Listen == "" {
		s.Listen = defaultSiteListen
	}
	if s.Title == "" {
		s.Title = "My site"
	}
	if s.Repo.Branch == "" {
		s.Repo.Branch = "main"
	}
	if !slices.Contains(s.Roles, "admin") {
		s.Roles = append([]string{"admin"}, s.Roles...)
	}
	if len(s.Roles) == 1 {
		s.Roles = append(s.Roles, "member")
	}
	return s
}

func (m *SiteManager) saveSettingsLocked() error {
	data, _ := json.MarshalIndent(m.settings, "", "  ")
	return writeFileAtomic(m.dir("settings.json"), append(data, '\n'), 0o600, true)
}

// Settings returns a copy of the current settings.
func (m *SiteManager) Settings() SiteSettings {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.settings
	s.Roles = slices.Clone(s.Roles)
	// [] rather than null in JSON.
	s.History = append([]SiteDeploy{}, s.History...)
	s.Builders = append([]string{}, s.Builders...)
	s.Proposals = append([]SiteProposal{}, s.Proposals...)
	return s
}

var (
	siteDomainRE = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	siteRoleRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	siteUserRE   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{1,39}$`)
	siteEmailRE  = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// SiteSettingsUpdate is what the dashboard may change directly.
type SiteSettingsUpdate struct {
	Enabled *bool   `json:"enabled"`
	Title   *string `json:"title"`
	Listen  *string `json:"listen"`
	Domain  *string `json:"domain"`
	Tunnel  *bool   `json:"tunnel"`
}

// UpdateSettings validates and saves a change, then starts, restarts or
// stops what the change affects.
func (m *SiteManager) UpdateSettings(u SiteSettingsUpdate) (SiteSettings, error) {
	m.mu.Lock()
	next := m.settings
	if u.Title != nil {
		t := strings.TrimSpace(*u.Title)
		if t == "" || len(t) > 80 {
			m.mu.Unlock()
			return SiteSettings{}, errors.New("the title must be 1 to 80 characters")
		}
		next.Title = t
	}
	if u.Listen != nil {
		host, port, err := net.SplitHostPort(strings.TrimSpace(*u.Listen))
		if err != nil || port == "" || port == "0" {
			m.mu.Unlock()
			return SiteSettings{}, errors.New(`listen must be host:port, e.g. "127.0.0.1:8780"`)
		}
		next.Listen = net.JoinHostPort(host, port)
	}
	if u.Domain != nil {
		d := strings.ToLower(strings.TrimSpace(*u.Domain))
		d = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://"), "/")
		if d != "" && !siteDomainRE.MatchString(d) {
			m.mu.Unlock()
			return SiteSettings{}, fmt.Errorf("%q is not a host name like example.com", d)
		}
		next.Domain = d
	}
	if u.Enabled != nil {
		next.Enabled = *u.Enabled
	}
	if u.Tunnel != nil {
		next.Tunnel = *u.Tunnel
		if next.Tunnel && !fileExists(m.dir("tunnel.token")) {
			m.mu.Unlock()
			return SiteSettings{}, errors.New("save a Cloudflare tunnel token first")
		}
	}
	restart := next.Listen != m.settings.Listen || next.Domain != m.settings.Domain || next.Title != m.settings.Title
	m.settings = next
	err := m.saveSettingsLocked()
	m.mu.Unlock()
	if err != nil {
		return SiteSettings{}, err
	}
	m.reconcile(restart)
	return m.Settings(), nil
}

// SetRoles replaces the role list. "admin" stays; a role someone has stays.
func (m *SiteManager) SetRoles(roles []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{"admin": true}
	out := []string{"admin"}
	for _, r := range roles {
		r = strings.TrimSpace(strings.ToLower(r))
		if r == "" || seen[r] {
			continue
		}
		if !siteRoleRE.MatchString(r) {
			return fmt.Errorf("%q: a role is lowercase letters, digits and -", r)
		}
		seen[r] = true
		out = append(out, r)
	}
	for _, u := range m.users {
		if !seen[u.Role] {
			return fmt.Errorf("%s still has the role %q; change it first", u.display(), u.Role)
		}
	}
	m.settings.Roles = out
	return m.saveSettingsLocked()
}

// ── accounts ────────────────────────────────────────────────────────────────

func (u SiteUser) display() string {
	if u.Kind == "google" {
		return u.Email
	}
	return u.Username
}

func (m *SiteManager) loadUsers() []SiteUser {
	var f struct {
		Users []SiteUser `json:"users"`
	}
	data, err := os.ReadFile(m.dir("users.json"))
	if err != nil {
		return nil
	}
	if err := json.Unmarshal(data, &f); err != nil {
		log.Printf("[site] %s is not valid JSON: %v", m.dir("users.json"), err)
	}
	return f.Users
}

func (m *SiteManager) saveUsersLocked() error {
	data, _ := json.MarshalIndent(map[string]any{"version": 1, "users": m.users}, "", "  ")
	return writeFileAtomic(m.dir("users.json"), append(data, '\n'), 0o600, true)
}

// Users returns the accounts without their password hashes.
func (m *SiteManager) Users() []SiteUser {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SiteUser, len(m.users))
	for i, u := range m.users {
		u.PasswordHash = ""
		out[i] = u
	}
	return out
}

// SiteUserInput adds or changes an account.
type SiteUserInput struct {
	Kind     string `json:"kind"`
	Email    string `json:"email"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Password string `json:"password"`
}

func (m *SiteManager) checkRoleLocked(kind, role string) error {
	if !slices.Contains(m.settings.Roles, role) {
		return fmt.Errorf("unknown role %q (roles: %s)", role, strings.Join(m.settings.Roles, ", "))
	}
	// Admins sign in with Google: a password is one more thing to guess or leak.
	if role == "admin" && kind != "google" {
		return errors.New("admins sign in with Google; give a password account another role")
	}
	return nil
}

// AddUser creates an account.
func (m *SiteManager) AddUser(in SiteUserInput) (SiteUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := SiteUser{ID: "u_" + randomToken(9), Kind: in.Kind, Name: strings.TrimSpace(in.Name), Role: in.Role, Created: time.Now().UTC().Format(time.RFC3339)}
	switch in.Kind {
	case "google":
		u.Email = strings.ToLower(strings.TrimSpace(in.Email))
		if !siteEmailRE.MatchString(u.Email) {
			return SiteUser{}, errors.New("enter the Google account's email address")
		}
		for _, x := range m.users {
			if x.Kind == "google" && x.Email == u.Email {
				return SiteUser{}, fmt.Errorf("%s already has an account", u.Email)
			}
		}
	case "password":
		u.Username = strings.TrimSpace(in.Username)
		if !siteUserRE.MatchString(u.Username) {
			return SiteUser{}, errors.New("a username is 2–40 letters, digits, . _ or -")
		}
		for _, x := range m.users {
			if x.Kind == "password" && strings.EqualFold(x.Username, u.Username) {
				return SiteUser{}, fmt.Errorf("the username %s is taken", u.Username)
			}
		}
		h, err := hashSitePassword(in.Password)
		if err != nil {
			return SiteUser{}, err
		}
		u.PasswordHash = h
	default:
		return SiteUser{}, errors.New(`kind must be "google" or "password"`)
	}
	if err := m.checkRoleLocked(u.Kind, u.Role); err != nil {
		return SiteUser{}, err
	}
	m.users = append(m.users, u)
	if err := m.saveUsersLocked(); err != nil {
		m.users = m.users[:len(m.users)-1]
		return SiteUser{}, err
	}
	u.PasswordHash = ""
	return u, nil
}

// UpdateUser changes an account's name, role or password.
func (m *SiteManager) UpdateUser(id string, in SiteUserInput) (SiteUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slices.IndexFunc(m.users, func(u SiteUser) bool { return u.ID == id })
	if i < 0 {
		return SiteUser{}, errors.New("no such account")
	}
	u := m.users[i]
	if in.Name != "" {
		u.Name = strings.TrimSpace(in.Name)
	}
	if in.Role != "" && in.Role != u.Role {
		if err := m.checkRoleLocked(u.Kind, in.Role); err != nil {
			return SiteUser{}, err
		}
		if u.Role == "admin" && m.adminCountLocked() == 1 {
			return SiteUser{}, errors.New("this is the only admin; make someone else admin first")
		}
		u.Role = in.Role
	}
	if in.Password != "" {
		if u.Kind != "password" {
			return SiteUser{}, errors.New("accounts that sign in with Google have no password here")
		}
		h, err := hashSitePassword(in.Password)
		if err != nil {
			return SiteUser{}, err
		}
		u.PasswordHash = h
	}
	prev := m.users[i]
	m.users[i] = u
	if err := m.saveUsersLocked(); err != nil {
		m.users[i] = prev
		return SiteUser{}, err
	}
	u.PasswordHash = ""
	return u, nil
}

// RemoveUser deletes an account. Its sessions stop working at once: the site
// looks every session's account up in users.json.
func (m *SiteManager) RemoveUser(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slices.IndexFunc(m.users, func(u SiteUser) bool { return u.ID == id })
	if i < 0 {
		return errors.New("no such account")
	}
	if m.users[i].Role == "admin" && m.adminCountLocked() == 1 && m.settings.Enabled {
		return errors.New("this is the only admin; add another admin or turn the site off first")
	}
	prev := slices.Clone(m.users)
	m.users = slices.Delete(m.users, i, i+1)
	if err := m.saveUsersLocked(); err != nil {
		m.users = prev
		return err
	}
	return nil
}

func (m *SiteManager) adminCountLocked() int {
	n := 0
	for _, u := range m.users {
		if u.Role == "admin" {
			n++
		}
	}
	return n
}

// hashSitePassword is the format the site process checks:
// pbkdf2-sha256$<iterations>$<salt b64>$<hash b64>.
func hashSitePassword(pw string) (string, error) {
	if len(pw) < 10 {
		return "", errors.New("a password needs at least 10 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, pw, salt, sitePBKDF2Iter, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", sitePBKDF2Iter, base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(key)), nil
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ── the site process ────────────────────────────────────────────────────────

// Start runs the site (and the tunnel) if they are turned on, and keeps them
// running until ctx ends.
func (m *SiteManager) Start(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	m.reconcile(false)
}

// Stop ends every process the site runs.
func (m *SiteManager) Stop() {
	m.site.stop()
	m.tunnel.stop()
	m.mu.Lock()
	previews := m.previews
	m.previews = map[string]*sitePreview{}
	m.mu.Unlock()
	for _, p := range previews {
		p.proc.stop()
	}
}

// reconcile starts or stops the site and tunnel to match the settings.
func (m *SiteManager) reconcile(restart bool) {
	m.mu.Lock()
	ctx, s := m.ctx, m.settings
	m.mu.Unlock()
	if ctx == nil {
		return // not started (tests, check command)
	}
	if s.Enabled && s.Approved != "" {
		if restart {
			m.site.stop()
		}
		m.site.ensure(ctx, m.siteCommand)
	} else {
		m.site.stop()
	}
	if s.Enabled && s.Tunnel {
		m.tunnel.ensure(ctx, m.tunnelCommand)
	} else {
		m.tunnel.stop()
	}
}

// baseContext is the bridge's context once started; background work such as
// a deploy outlives the request that began it.
func (m *SiteManager) baseContext() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx != nil {
		return m.ctx
	}
	return context.Background()
}

// Restart restarts the site process (after a deploy, or from the dashboard).
func (m *SiteManager) Restart() {
	m.site.stop()
	m.reconcile(false)
}

// siteRuntime is runtime.json, the site process's whole configuration.
type siteRuntime struct {
	Listen           string `json:"listen"`
	PublicOrigin     string `json:"public_origin"`
	Title            string `json:"title"`
	ModulesDir       string `json:"modules_dir"`
	UsersFile        string `json:"users_file"`
	SessionKeyFile   string `json:"session_key_file"`
	GoogleClientFile string `json:"google_client_file"`
	PreviewLabel     string `json:"preview_label,omitempty"`
	PreviewsFile     string `json:"previews_file,omitempty"`
}

// googleClientFile is the site's own Google client if one is saved, else
// Fridolin's shared one.
func (m *SiteManager) googleClientFile() string {
	if p := m.dir("google_client.json"); fileExists(p) {
		return p
	}
	if m.googleClient != nil {
		return m.googleClient()
	}
	return ""
}

func (m *SiteManager) sessionKeyFile() (string, error) {
	p := m.dir("session.key")
	if fileExists(p) {
		return p, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return p, writeFileAtomic(p, []byte(hex.EncodeToString(key)+"\n"), 0o600, false)
}

// processCommand builds the node command for a site process serving modulesDir.
func (m *SiteManager) processCommand(ctx context.Context, name, listen, modulesDir, label string) (*exec.Cmd, error) {
	s := m.Settings()
	shell, err := m.ensureShell(ctx)
	if err != nil {
		return nil, err
	}
	node, err := findNode()
	if err != nil {
		return nil, err
	}
	keyFile, err := m.sessionKeyFile()
	if err != nil {
		return nil, err
	}
	rt := siteRuntime{
		Listen: listen, Title: s.Title, ModulesDir: modulesDir,
		UsersFile: m.dir("users.json"), SessionKeyFile: keyFile,
		GoogleClientFile: m.googleClientFile(), PreviewLabel: label,
	}
	if label == "" {
		rt.PreviewsFile = m.dir("previews.json")
		if s.Domain != "" {
			rt.PublicOrigin = "https://" + s.Domain
		}
	}
	if !fileExists(rt.UsersFile) {
		m.mu.Lock()
		err := m.saveUsersLocked()
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	data, _ := json.MarshalIndent(rt, "", "  ")
	rtFile := m.dir(name + ".runtime.json")
	if err := writeFileAtomic(rtFile, data, 0o600, false); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, node, "--import", "tsx/esm", filepath.Join(shell, "server", "main.ts"))
	cmd.Dir = shell
	cmd.Env = siteEnv(node, "FRIDOLIN_SITE_RUNTIME="+rtFile, "NODE_ENV=production")
	return cmd, nil
}

func (m *SiteManager) siteCommand(ctx context.Context) (*exec.Cmd, error) {
	s := m.Settings()
	return m.processCommand(ctx, "site", s.Listen, m.modulesDirOf(s.Approved), "")
}

// tunnelCommand runs a Cloudflare tunnel whose public host name points at the
// site's listen address (set up in Cloudflare). The token goes in through the
// environment, not the command line, so other users' ps cannot see it.
func (m *SiteManager) tunnelCommand(ctx context.Context) (*exec.Cmd, error) {
	token, err := os.ReadFile(m.dir("tunnel.token"))
	if err != nil {
		return nil, errors.New("no tunnel token saved")
	}
	bin, err := findTool("cloudflared")
	if err != nil {
		return nil, errors.New("cloudflared is not installed (macOS: brew install cloudflared)")
	}
	cmd := exec.CommandContext(ctx, bin, "tunnel", "--no-autoupdate", "run")
	cmd.Env = siteEnv(bin, "TUNNEL_TOKEN="+strings.TrimSpace(string(token)))
	return cmd, nil
}

// SetTunnelToken saves (or with "" removes) the Cloudflare tunnel token.
func (m *SiteManager) SetTunnelToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		m.mu.Lock()
		m.settings.Tunnel = false
		err := m.saveSettingsLocked()
		m.mu.Unlock()
		m.tunnel.stop()
		if err != nil {
			return err
		}
		if err := os.Remove(m.dir("tunnel.token")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if strings.ContainsAny(token, " \t\n") || len(token) < 40 {
		return errors.New("that does not look like a tunnel token (Cloudflare: Zero Trust → Networks → Tunnels → your tunnel → install command, the long value after --token)")
	}
	if err := writeFileAtomic(m.dir("tunnel.token"), []byte(token+"\n"), 0o600, false); err != nil {
		return err
	}
	m.reconcile(true)
	return nil
}

// SetGoogleClient saves a Google OAuth client just for the site (or with nil
// data removes it, falling back to Fridolin's shared client).
func (m *SiteManager) SetGoogleClient(data []byte) error {
	if data == nil {
		if err := os.Remove(m.dir("google_client.json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	c, err := ParseGoogleClient(data)
	if err != nil {
		return err
	}
	if c.Type != "web" {
		return errors.New(`the site needs a "Web application" client (this one is a Desktop app client)`)
	}
	return writeFileAtomic(m.dir("google_client.json"), data, 0o600, false)
}

// siteEnv is the whole environment of a site process: nothing of the bridge's
// own environment (tokens, API keys) reaches it.
func siteEnv(bin string, extra ...string) []string {
	home, _ := os.UserHomeDir()
	env := []string{
		"HOME=" + home,
		"PATH=" + filepath.Dir(bin) + ":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin",
		"LANG=en_US.UTF-8",
	}
	for _, k := range []string{"USER", "LOGNAME", "TMPDIR"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return append(env, extra...)
}

// findNode finds Node.js (the site needs 22 or newer): on PATH, in the usual
// folders, or the newest nvm install. Symlinks are resolved, so npm is found
// next to it.
func findNode() (string, error) {
	bin, err := findTool("node")
	if err != nil {
		home, _ := os.UserHomeDir()
		found, _ := filepath.Glob(filepath.Join(home, ".nvm/versions/node/v*/bin/node"))
		slices.SortFunc(found, func(a, b string) int { return compareVersionDirs(b, a) })
		if len(found) == 0 {
			return "", errors.New("node is not installed (macOS: brew install node); the site needs Node.js 22 or newer")
		}
		bin = found[0]
	}
	if real, err := filepath.EvalSymlinks(bin); err == nil {
		bin = real
	}
	return bin, nil
}

// compareVersionDirs orders …/v22.14.0/bin/node before …/v24.1.0/bin/node.
func compareVersionDirs(a, b string) int {
	va := strings.Split(strings.TrimPrefix(filepath.Base(filepath.Dir(filepath.Dir(a))), "v"), ".")
	vb := strings.Split(strings.TrimPrefix(filepath.Base(filepath.Dir(filepath.Dir(b))), "v"), ".")
	for i := 0; i < len(va) && i < len(vb); i++ {
		var x, y int
		fmt.Sscan(va[i], &x)
		fmt.Sscan(vb[i], &y)
		if x != y {
			return x - y
		}
	}
	return len(va) - len(vb)
}

// findTool looks on PATH and in the usual install folders (launchd's PATH is short).
func findTool(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".local/bin")} {
		if p := filepath.Join(dir, name); fileExists(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found", name)
}

// ensureShell unpacks the embedded site process into <data_dir>/site/shell
// when this binary's copy differs, and installs its dependencies.
func (m *SiteManager) ensureShell(ctx context.Context) (string, error) {
	dir := m.dir("shell")
	h := sha256.New()
	var files []string
	err := fs.WalkDir(siteShellFS, "site", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == "site/template" || p == "site/builder" || p == "site/test" {
				return fs.SkipDir
			}
			return nil
		}
		data, err := siteShellFS.ReadFile(p)
		if err != nil {
			return err
		}
		h.Write([]byte(p + "\x00"))
		h.Write(data)
		files = append(files, p)
		return nil
	})
	if err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	stamp := filepath.Join(dir, ".fridolin-shell")
	if prev, err := os.ReadFile(stamp); err == nil && strings.TrimSpace(string(prev)) == sum && fileExists(filepath.Join(dir, "node_modules")) {
		return dir, nil
	}
	for _, p := range files {
		data, _ := siteShellFS.ReadFile(p)
		dst := filepath.Join(dir, strings.TrimPrefix(p, "site/"))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return "", err
		}
	}
	if err := m.npmInstall(ctx, dir, true); err != nil {
		return "", fmt.Errorf("installing the site's dependencies: %w", err)
	}
	return dir, os.WriteFile(stamp, []byte(sum+"\n"), 0o600)
}

// runNpmInstall installs a package's dependencies (npm ci when there is a lock file).
func (m *SiteManager) runNpmInstall(ctx context.Context, dir string, prod bool) error {
	node, err := findNode()
	if err != nil {
		return err
	}
	npm := filepath.Join(filepath.Dir(node), "npm")
	if !fileExists(npm) {
		if npm, err = findTool("npm"); err != nil {
			return errors.New("npm not found next to node")
		}
	}
	args := []string{"install"}
	if fileExists(filepath.Join(dir, "package-lock.json")) {
		args = []string{"ci"}
	}
	args = append(args, "--no-audit", "--no-fund")
	if prod {
		args = append(args, "--omit=dev")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, npm, args...)
	cmd.Dir = dir
	cmd.Env = siteEnv(node)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("npm %s: %v\n%s", args[0], err, tail(string(out), 1500))
	}
	return nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// SiteProcStatus is what the dashboard shows about one process.
type SiteProcStatus struct {
	State    string   `json:"state"` // stopped, starting, running, crashed
	Since    string   `json:"since,omitempty"`
	Error    string   `json:"error,omitempty"`
	Restarts int      `json:"restarts"`
	Log      []string `json:"log"`
}

// siteProc keeps one process running: it restarts it after a crash, waiting
// longer each time, until stop.
type siteProc struct {
	name string

	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	state    string
	since    time.Time
	lastErr  string
	restarts int
	lines    []string
}

func newSiteProc(name string) *siteProc { return &siteProc{name: name, state: "stopped"} }

func (p *siteProc) logLine(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lines = append(p.lines, line)
	if len(p.lines) > 300 {
		p.lines = p.lines[len(p.lines)-300:]
	}
}

func (p *siteProc) set(state, errText string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state, p.lastErr, p.since = state, errText, time.Now()
}

func (p *siteProc) status() SiteProcStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := SiteProcStatus{State: p.state, Error: p.lastErr, Restarts: p.restarts, Log: slices.Clone(p.lines)}
	if !p.since.IsZero() {
		st.Since = p.since.UTC().Format(time.RFC3339)
	}
	if len(st.Log) > 80 {
		st.Log = st.Log[len(st.Log)-80:]
	}
	return st
}

// ensure starts the supervisor unless it is already running.
func (p *siteProc) ensure(parent context.Context, build func(context.Context) (*exec.Cmd, error)) {
	p.mu.Lock()
	if p.cancel != nil {
		p.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	p.cancel, p.done, p.restarts = cancel, make(chan struct{}), 0
	p.mu.Unlock()
	go p.supervise(ctx, build)
}

// stop ends the process and waits for it.
func (p *siteProc) stop() {
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.cancel = nil
	p.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	p.set("stopped", "")
}

func (p *siteProc) supervise(ctx context.Context, build func(context.Context) (*exec.Cmd, error)) {
	defer close(p.done)
	delay := time.Second
	for ctx.Err() == nil {
		p.set("starting", "")
		started := time.Now()
		err := p.runOnce(ctx, build)
		if ctx.Err() != nil {
			return
		}
		msg := "exited"
		if err != nil {
			msg = err.Error()
		}
		log.Printf("[site] %s %s — restarting in %v", p.name, msg, delay)
		p.set("crashed", msg)
		p.mu.Lock()
		p.restarts++
		p.mu.Unlock()
		if time.Since(started) > time.Minute {
			delay = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < time.Minute {
			delay *= 2
		}
	}
}

func (p *siteProc) runOnce(ctx context.Context, build func(context.Context) (*exec.Cmd, error)) error {
	cmd, err := build(ctx)
	if err != nil {
		p.logLine("! " + err.Error())
		return err
	}
	isolateProcessGroup(cmd)
	cmd.Cancel = func() error {
		// Ask nicely first; WaitDelay escalates to SIGKILL.
		_ = cmd.Process.Signal(os.Interrupt)
		return nil
	}
	cmd.WaitDelay = 5 * time.Second
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			p.logLine(sc.Text())
		}
	}()
	if err := cmd.Start(); err != nil {
		pw.Close()
		return err
	}
	p.set("running", "")
	err = cmd.Wait()
	pw.Close()
	if cmd.Process != nil {
		_ = killProcessGroup(cmd.Process.Pid)
	}
	return err
}

// healthy reports whether a site process answers on listen.
func siteHealthy(listen string) bool {
	c := &http.Client{Timeout: 700 * time.Millisecond}
	resp, err := c.Get("http://" + listen + "/_fridolin/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
