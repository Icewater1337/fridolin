package site

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
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/sandbox"
	"github.com/Icewater1337/fridolin/internal/textx"
	"github.com/Icewater1337/fridolin/site"
)

const (
	defaultSiteListen = "127.0.0.1:8780"
	sitePBKDF2Iter    = 600_000
	siteKeepDeploys   = 3
)

// SiteManager runs the site.
type SiteManager struct {
	dataDir      string
	host         SiteHost
	googleClient func() string // path of Fridolin's shared Google OAuth client file

	// Selected at construction: installing npm dependencies into a folder,
	// and into a builder brain's workspace (as that brain).
	npmInstall       func(ctx context.Context, dir string, prod bool) error
	workspaceInstall func(ctx context.Context, brainID, dir string) error
	// sandboxExec runs the live site under its profile (site_sandbox.go).
	sandboxExec string
	// container: the live site listens on the container's interfaces.
	container bool

	secretsMu sync.Mutex // site_secrets.go

	mu       sync.Mutex
	settings SiteSettings
	users    []SiteUser
	site     *siteProc
	tunnel   *siteProc
	deploy   *SiteJob
	previews map[string]*sitePreview
	tokens   map[string]string // builder token → brain id
	hooks    SiteBuilderHooks
	chats    *siteChats
	// searchSlots limits the searches the bridge runs for the site at once.
	searchSlots chan struct{}
	ctx         context.Context

	warnNoSandbox sync.Once

	// For tests: where CheckDomain asks, and with what (site_domain.go).
	domainURL    func(domain string) string
	domainClient *http.Client
}

// NewSiteManager loads the site's settings and accounts from opts.DataDir/site.
func New(opts SiteOptions) *SiteManager {
	m := &SiteManager{
		dataDir:      opts.DataDir,
		host:         opts.Host,
		googleClient: opts.GoogleClient,
		hooks:        opts.Builder,
		container:    opts.Container,
		previews:     map[string]*sitePreview{},
		site:         newSiteProc("site"),
		tunnel:       newSiteProc("tunnel"),
		sandboxExec:  opts.SandboxExec,
	}
	if m.sandboxExec == "" {
		m.sandboxExec = sandbox.ExecPath
	}
	m.npmInstall = opts.NPMInstall
	if m.npmInstall == nil {
		m.npmInstall = m.runNpmInstall
	}
	m.workspaceInstall = opts.WorkspaceInstall
	if m.workspaceInstall == nil {
		m.workspaceInstall = m.npmInstallAsBrain
	}
	m.chats = &siteChats{m: m}
	m.searchSlots = make(chan struct{}, spoolSearchLimit)
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
	if s.RoleBrains == nil {
		s.RoleBrains = map[string][]string{}
	}
	return s
}

func (m *SiteManager) saveSettingsLocked() error {
	data, _ := json.MarshalIndent(m.settings, "", "  ")
	return fsx.WriteFileAtomic(m.dir("settings.json"), append(data, '\n'), 0o600, true)
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
	s.RoleBrains = map[string][]string{}
	for role, brains := range m.settings.RoleBrains {
		s.RoleBrains[role] = slices.Clone(brains)
	}
	return s
}

var (
	siteDomainRE = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	siteRoleRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	siteUserRE   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{1,39}$`)
	siteEmailRE  = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

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
		if next.Tunnel && !fsx.Exists(m.dir("tunnel.token")) {
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
			return fmt.Errorf("%s still has the role %q; change it first", u.Display(), u.Role)
		}
	}
	m.settings.Roles = out
	for role := range m.settings.RoleBrains {
		if !seen[role] || role == "admin" {
			delete(m.settings.RoleBrains, role)
		}
	}
	return m.saveSettingsLocked()
}

// siteBrainRE is a brain's id, as manifest.go checks it.
var siteBrainRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// SetRoleBrains sets the brains each role sees on the site's /files and
// /wiki; a role left out keeps what it had. admin sees every brain and
// takes no list.
func (m *SiteManager) SetRoleBrains(brains map[string][]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := map[string][]string{}
	for role, list := range m.settings.RoleBrains {
		next[role] = list
	}
	for role, list := range brains {
		if role == "admin" {
			return errors.New("admin sees every brain; give the other roles their brains")
		}
		if !slices.Contains(m.settings.Roles, role) {
			return fmt.Errorf("unknown role %q (roles: %s)", role, strings.Join(m.settings.Roles, ", "))
		}
		clean := []string{}
		for _, id := range list {
			if !siteBrainRE.MatchString(id) {
				return fmt.Errorf("%q is not a brain id", id)
			}
			if !slices.Contains(clean, id) {
				clean = append(clean, id)
			}
		}
		slices.Sort(clean)
		if len(clean) == 0 {
			delete(next, role)
			continue
		}
		next[role] = clean
	}
	prev := m.settings.RoleBrains
	m.settings.RoleBrains = next
	if err := m.saveSettingsLocked(); err != nil {
		m.settings.RoleBrains = prev
		return err
	}
	return nil
}

// ── accounts ────────────────────────────────────────────────────────────────

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
	return fsx.WriteFileAtomic(m.dir("users.json"), append(data, '\n'), 0o600, true)
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
	u := SiteUser{ID: "u_" + siteRandomToken(9), Kind: in.Kind, Name: strings.TrimSpace(in.Name), Role: in.Role, Created: time.Now().UTC().Format(time.RFC3339)}
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
		h, err := newSitePasswordHash(in)
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

// newSitePasswordHash is what a new password account keeps: Fridolin's hash
// of its password, or a bcrypt hash brought over from another app.
func newSitePasswordHash(in SiteUserInput) (string, error) {
	switch {
	case in.PasswordHash == "":
		return hashSitePassword(in.Password)
	case in.Password != "":
		return "", errors.New("give a password or a password_hash, not both")
	}
	return checkBroughtHash(in.PasswordHash)
}

// siteBcryptRE is a bcrypt hash as Node's bcrypt and bcryptjs, Python's
// bcrypt, PHP and htpasswd -B write it: version, two-digit cost, then 22
// characters of salt and 31 of hash.
var siteBcryptRE = regexp.MustCompile(`^\$2[aby]\$([0-9]{2})\$[./A-Za-z0-9]{53}$`)

// Bcrypt costs the site takes: below 10 is quick to crack; each step up
// doubles the time a sign-in takes, and above 14 that is seconds.
const (
	siteBcryptMinCost = 10
	siteBcryptMaxCost = 14
)

func checkBroughtHash(h string) (string, error) {
	m := siteBcryptRE.FindStringSubmatch(h)
	if m == nil {
		return "", errors.New("password_hash must be a bcrypt hash ($2a$, $2b$ or $2y$)")
	}
	if cost, _ := strconv.Atoi(m[1]); cost < siteBcryptMinCost || cost > siteBcryptMaxCost {
		return "", fmt.Errorf("password_hash has cost %d; the site takes bcrypt costs %d to %d", cost, siteBcryptMinCost, siteBcryptMaxCost)
	}
	return h, nil
}

// ── the site process ────────────────────────────────────────────────────────

// Start runs the site (and the tunnel) if they are turned on, and keeps them
// running until ctx ends.
func (m *SiteManager) Start(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	go m.watchSpool(ctx)
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
	if s.Enabled {
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

// BaseContext is the bridge's context once started; background work such as
// a deploy outlives the request that began it.
func (m *SiteManager) BaseContext() context.Context {
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
	// SecretsFD is the file descriptor the live site reads its modules'
	// secrets from at startup (site_secrets.go).
	SecretsFD int `json:"secrets_fd,omitempty"`
	// DataDir holds one folder per module (ctx.dataDir); SpoolDir is the
	// handoff to brains (ctx.ingest, site_ingest.go). The live site only.
	DataDir  string `json:"data_dir,omitempty"`
	SpoolDir string `json:"spool_dir,omitempty"`
	// DevicesFile lists the paired phones' public keys, so the live site
	// turns away an upload no paired phone signed (plaud_app.go).
	DevicesFile string `json:"devices_file,omitempty"`
	// BridgeURL is the bridge's site listener, which serves the built-in
	// /files and /wiki; its token comes through the secrets pipe. The live
	// site only.
	BridgeURL string `json:"bridge_url,omitempty"`
	// Container: the live site binds listen's port on every interface of
	// the fridolin-core container, since Docker forwards a published port
	// to the container's own address, not to its loopback; and it trusts
	// the client address Docker's private addresses forward, as it trusts
	// cloudflared's on the loopback.
	Container bool `json:"container,omitempty"`
}

// googleClientFile is the site's own Google client if one is saved, else
// Fridolin's shared one. It goes into runtime.json when the site starts, so
// saving or removing the site's own client restarts the site; the site reads
// the file at each sign-in, so a changed shared client needs no restart.
func (m *SiteManager) googleClientFile() string {
	if p := m.dir("google_client.json"); fsx.Exists(p) {
		return p
	}
	if m.googleClient != nil {
		return m.googleClient()
	}
	return ""
}

func (m *SiteManager) sessionKeyFile() (string, error) {
	p := m.dir("session.key")
	if fsx.Exists(p) {
		return p, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return p, fsx.WriteFileAtomic(p, []byte(hex.EncodeToString(key)+"\n"), 0o600, false)
}

// processCommand builds the node command for a site process serving
// modulesDir. The live site (no label) runs under its sandbox where there is
// one, and gets its modules' secrets on file descriptor 3.
func (m *SiteManager) processCommand(ctx context.Context, name, listen, modulesDir, label string) (*exec.Cmd, error) {
	s := m.Settings()
	shell, err := m.ensureShell(ctx)
	if err != nil {
		return nil, err
	}
	node, err := m.host.Node()
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
	live := label == ""
	var mods []sandbox.SiteModule
	if live {
		rt.PreviewsFile = m.dir("previews.json")
		if s.Domain != "" {
			rt.PublicOrigin = "https://" + s.Domain
		}
		mods = readModuleCaps(modulesDir, m.host.Home())
		rt.SecretsFD = 3
		rt.DataDir, rt.SpoolDir, rt.DevicesFile = m.dir("data"), m.spoolDir(), m.dir("devices.json")
		rt.Container = m.container
		for _, d := range []string{rt.SpoolDir, rt.DataDir} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				return nil, err
			}
		}
		// The site may write in them but not make them: the sandbox opens
		// data/ and what is below it, not data/ itself.
		for _, mod := range mods {
			if err := os.MkdirAll(filepath.Join(rt.DataDir, mod.ID), 0o700); err != nil {
				return nil, err
			}
		}
	}
	if !fsx.Exists(rt.UsersFile) {
		m.mu.Lock()
		err := m.saveUsersLocked()
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	tmp := m.dir("tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, err
	}
	var bridgeToken string
	if live && m.hooks.FilesBridge != nil {
		url, token, err := m.hooks.FilesBridge()
		if err != nil {
			log.Printf("[site] the built-in /files and /wiki are off: %v", err)
		} else {
			rt.BridgeURL, bridgeToken = url, token
		}
	}
	if live && s.Approved == "" {
		if err := os.MkdirAll(modulesDir, 0o700); err != nil {
			return nil, err
		}
	}
	data, _ := json.MarshalIndent(rt, "", "  ")
	rtFile := m.dir(name + ".runtime.json")
	if err := fsx.WriteFileAtomic(rtFile, data, 0o600, false); err != nil {
		return nil, err
	}
	bin, args := node, []string{"--import", "tsx/esm", filepath.Join(shell, "server", "main.ts")}
	if live {
		// Without an approved commit the site serves its built-in pages
		// alone, from an empty deploy.
		deploy := m.dir("deploys", "none")
		root := deploy
		if s.Approved != "" {
			deploy = m.deployDir(s.Approved)
			root = filepath.Join(deploy, filepath.FromSlash(s.Repo.Subdir))
		}
		in := m.sandboxInput(node, deploy, root, rtFile, mods)
		prepareDeclaredFolders(mods, in.Unwritable)
		switch err := siteSandboxAvailable(m.sandboxExec); {
		case err == nil:
			profile, err := m.host.Profile(in)
			if err != nil {
				return nil, fmt.Errorf("the site's sandbox: %w", err)
			}
			bin, args = m.sandboxExec, append([]string{"-p", profile, node}, args...)
		case runtime.GOOS == "darwin":
			// Never run approved code unsandboxed where the sandbox should be.
			return nil, err
		default:
			m.warnNoSandbox.Do(func() { log.Printf("[site] %v", err) })
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = shell
	cmd.Env = m.host.Env(node, "FRIDOLIN_SITE_RUNTIME="+rtFile, "NODE_ENV=production", "TMPDIR="+tmp+string(filepath.Separator))
	if live {
		secrets, err := m.secretsFor(mods)
		if err != nil {
			return nil, err
		}
		if bridgeToken != "" {
			if secrets, err = withBridgeToken(secrets, bridgeToken); err != nil {
				return nil, err
			}
		}
		r, err := pipeWith(secrets)
		if err != nil {
			return nil, err
		}
		cmd.ExtraFiles = []*os.File{r} // fd 3; runOnce closes this end after Start
	}
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
	bin, err := m.host.Tool("cloudflared")
	if err != nil {
		return nil, errors.New("cloudflared is not installed (macOS: brew install cloudflared)")
	}
	cmd := exec.CommandContext(ctx, bin, "tunnel", "--no-autoupdate", "run")
	cmd.Env = m.host.Env(bin, "TUNNEL_TOKEN="+strings.TrimSpace(string(token)))
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
	if err := fsx.WriteFileAtomic(m.dir("tunnel.token"), []byte(token+"\n"), 0o600, false); err != nil {
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
	clientType, err := m.host.GoogleClientType(data)
	if err != nil {
		return err
	}
	if clientType != "web" {
		return errors.New(`the site needs a "Web application" client (this one is a Desktop app client)`)
	}
	return fsx.WriteFileAtomic(m.dir("google_client.json"), data, 0o600, false)
}

// ensureShell unpacks the embedded site process into <data_dir>/site/shell
// when this binary's copy differs, and installs its dependencies.
func (m *SiteManager) ensureShell(ctx context.Context) (string, error) {
	dir := m.dir("shell")
	h := sha256.New()
	var files []string
	err := fs.WalkDir(site.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == "template" || p == "builder" || p == "test" {
				return fs.SkipDir
			}
			return nil
		}
		data, err := site.FS.ReadFile(p)
		if err != nil {
			return err
		}
		// Named as from the checkout's root, as when the files were
		// embedded there: the same files give the same stamp, so an
		// upgrade does not install the dependencies again for nothing.
		h.Write([]byte("site/" + p + "\x00"))
		h.Write(data)
		files = append(files, p)
		return nil
	})
	if err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	stamp := filepath.Join(dir, ".fridolin-shell")
	if prev, err := os.ReadFile(stamp); err == nil && strings.TrimSpace(string(prev)) == sum && fsx.Exists(filepath.Join(dir, "node_modules")) {
		return dir, nil
	}
	for _, p := range files {
		data, _ := site.FS.ReadFile(p)
		dst := filepath.Join(dir, p)
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
	node, err := m.host.Node()
	if err != nil {
		return err
	}
	npm := filepath.Join(filepath.Dir(node), "npm")
	if !fsx.Exists(npm) {
		if npm, err = m.host.Tool("npm"); err != nil {
			return errors.New("npm not found next to node")
		}
	}
	args := []string{"install"}
	if fsx.Exists(filepath.Join(dir, "package-lock.json")) {
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
	cmd.Env = m.host.Env(node)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("npm %s: %v\n%s", args[0], err, textx.Tail(string(out), 1500))
	}
	return nil
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
	platform.IsolateProcessGroup(cmd)
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
	err = cmd.Start()
	// The child has its own copies now (the live site's secrets pipe).
	for _, f := range cmd.ExtraFiles {
		f.Close()
	}
	if err != nil {
		pw.Close()
		return err
	}
	p.set("running", "")
	err = cmd.Wait()
	pw.Close()
	if cmd.Process != nil {
		_ = platform.KillProcessGroup(cmd.Process.Pid)
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
