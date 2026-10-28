package httpapi

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

type Deps struct {
	Listen            string
	Ready             http.HandlerFunc
	Send              http.HandlerFunc
	Ingest            http.HandlerFunc
	TakeOTP           http.HandlerFunc
	Builder           func() http.Handler
	Brain             func() http.Handler
	DashboardDisabled func() bool
	Static            fs.FS
	GoogleCallback    http.HandlerFunc
	GitHub            func() *GitHubAPI
	Login             http.HandlerFunc
	Token             *Token
	RequiresToken     func() bool
	SignedIn          func(*http.Request) bool
	API               func() http.Handler
	// FilesRaw serves /files/raw: a file by a ticket in its address, which
	// the page's <img>, <video> and download links need (files_ticket.go).
	FilesRaw http.HandlerFunc
	// Remote serves the surface on the tailnet instead of this machine's
	// loopback: Hosts are the Host values it answers to (the node's
	// MagicDNS names), the dashboard API always wants the token or a
	// session whatever dashboard.require_token says, /send and /ingest
	// want a token, and the brain and builder APIs, which only local runs
	// call, are not served.
	Remote bool
	Hosts  map[string]bool
}

// newHTTPHandler builds the whole HTTP surface.
func New(d Deps) http.Handler {
	hosts := AllowedHosts(d.Listen)
	if d.Remote {
		hosts = d.Hosts
		d.RequiresToken = func() bool { return true }
		d.Send, d.Ingest = RequireAuthorization(d.Send), RequireAuthorization(d.Ingest)
	}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /ready", d.Ready)
	mux.HandleFunc("POST /send", d.Send)
	mux.HandleFunc("POST /ingest", d.Ingest)
	if !d.Remote {
		// One-time codes for local scripts; never on the tailnet.
		mux.HandleFunc("POST /otp/take", d.TakeOTP)
		// The site builder's MCP servers; each brain's own token (site_builder.go).
		mux.Handle("/builder/", d.Builder())
		// The tools brains run; each run's own token (brainapi.go).
		mux.Handle("/brain/", d.Brain())
	}

	if !d.DashboardDisabled() {
		static := StaticHandler(d.Static)
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			// Google sign-in returns here (the redirect URI is the dashboard
			// itself). The one-time state ties it to a sign-in the dashboard
			// started, so it needs no token. Google sends the browser back
			// with a GET; a HEAD, say from a link prefetcher, must not use
			// up the state.
			if r.Method == http.MethodGet && r.URL.Path == "/" && IsOAuthCallback(r) {
				d.GoogleCallback(w, r)
				return
			}
			static.ServeHTTP(w, r)
		}))
		// The GitHub App's manifest flow (github.go): pages the browser is
		// sent to, each tied to a one-time state rather than a token.
		mux.HandleFunc("GET /github/connect", d.GitHub().Handoff)
		mux.HandleFunc("GET /github/created", d.GitHub().Created)
		mux.HandleFunc("GET /github/installed", d.GitHub().Installed)
		mux.HandleFunc("POST /api/login", d.Login)
		mux.HandleFunc("GET /files/raw", d.FilesRaw)
		mux.Handle("/api/", APIGuard(d.Token, d.RequiresToken, d.SignedIn, d.API()))
	}

	schemes := []string{"http://"}
	if d.Remote {
		schemes = append(schemes, "https://")
	}
	return SecurityHeaders(HostGuard(hosts, originGuard(hosts, schemes, mux)))
}

type APIDeps struct {
	Status       http.HandlerFunc
	Activity     http.HandlerFunc
	Logs         http.HandlerFunc
	Restart      http.HandlerFunc
	Site         func(*http.ServeMux)
	DevMode      bool
	Connection   func() *ConnectionAPI
	Chats        func() *ChatsAPI
	Config       func() *ConfigAPI
	Brains       func() *BrainsAPI
	Filesystem   func() *FilesystemAPI
	Backup       func() *BackupAPI
	Vault        func() *VaultAPI
	Google       func() *GoogleAPI
	Plaud        func() *PlaudAPI
	PlaudApp     func() *PlaudAppAPI
	GitHub       func() *GitHubAPI
	Auth         func() *DashboardAuth
	Dev          func() *DevAPI
	Nodes        func() *NodesAPI
	Policy       func() *PolicyAPI
	Housekeeping func() *HousekeepingAPI
	Files        func() *FilesAPI
	Schedules    func() *SchedulesAPI
	Connections  func() *ConnectionsAPI
	// GitIdentities are the GitLab connection and what the FrIDEs get.
	GitIdentities func() *GitIdentitiesAPI
}

func NewAPIMux(d APIDeps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/status", d.Status)
	mux.HandleFunc("GET /api/whatsapp/qr", d.Connection().QR)
	mux.HandleFunc("POST /api/whatsapp/pair", d.Connection().Pair)
	mux.HandleFunc("GET /api/whatsapp/groups", d.Connection().Groups)
	mux.HandleFunc("GET /api/activity", d.Activity)
	mux.HandleFunc("GET /api/logs", d.Logs)

	mux.HandleFunc("GET /api/chats/ignored", d.Chats().Ignored)
	mux.HandleFunc("POST /api/chats/allow", d.Chats().Allow)
	mux.HandleFunc("POST /api/chats/revoke", d.Chats().Revoke)
	mux.HandleFunc("POST /api/chats/members", d.Chats().Members)
	mux.HandleFunc("POST /api/chats/dismiss", d.Chats().Dismiss)

	mux.HandleFunc("GET /api/config", d.Config().Get)
	mux.HandleFunc("PUT /api/config", d.Config().Put)
	mux.HandleFunc("POST /api/restart", d.Restart)

	mux.HandleFunc("POST /api/logout", d.Auth().Logout)
	mux.HandleFunc("POST /api/login/code", d.Auth().LoginCode)
	mux.HandleFunc("POST /api/token/rotate", d.Auth().RotateToken)

	mux.HandleFunc("GET /api/brains", d.Brains().List)
	mux.HandleFunc("GET /api/brains/{id}", d.Brains().Get)
	mux.HandleFunc("POST /api/brains/inspect", d.Brains().Inspect)
	mux.HandleFunc("POST /api/brains", d.Brains().Add)
	mux.HandleFunc("PUT /api/brains/{id}", d.Brains().Update)
	mux.HandleFunc("POST /api/brains/{id}/approve", d.Brains().Approve)
	mux.HandleFunc("DELETE /api/brains/{id}", d.Brains().Remove)
	mux.HandleFunc("DELETE /api/brains/{id}/intents/{intent}", d.Brains().RemoveIntentOf)
	mux.HandleFunc("POST /api/brains/{id}/stop", d.Connection().StopRun)
	mux.HandleFunc("DELETE /api/brain-dirs", d.Brains().RemoveDir)

	mux.HandleFunc("GET /api/fs/capabilities", d.Filesystem().Caps)
	mux.HandleFunc("POST /api/fs/pick", d.Filesystem().Pick)
	mux.HandleFunc("GET /api/fs/list", d.Filesystem().List)

	mux.HandleFunc("GET /api/backup/status", d.Backup().Status)
	mux.HandleFunc("POST /api/backup/connect", d.Backup().Connect)
	mux.HandleFunc("POST /api/backup/disconnect", d.Backup().Disconnect)
	mux.HandleFunc("PUT /api/backup/drive-client", d.Backup().SetDriveClient)
	mux.HandleFunc("DELETE /api/backup/drive-client", d.Backup().DeleteDriveClient)
	mux.HandleFunc("PUT /api/backup/settings", d.Backup().Settings)
	mux.HandleFunc("POST /api/backup/run", d.Backup().Run)
	mux.HandleFunc("POST /api/backup/cancel", d.Backup().Cancel)
	mux.HandleFunc("POST /api/backup/restore", d.Backup().Restore)
	mux.HandleFunc("PUT /api/brains/{id}/backup", d.Backup().BrainSettings)
	mux.HandleFunc("GET /api/brains/{id}/backup/versions", d.Backup().Versions)

	mux.HandleFunc("GET /api/vault/status", d.Vault().Status)
	mux.HandleFunc("POST /api/vault/sync", d.Vault().Sync)
	mux.HandleFunc("POST /api/vault/evict", d.Vault().Evict)
	mux.HandleFunc("POST /api/vault/rebuild", d.Vault().Rebuild)
	mux.HandleFunc("POST /api/vault/get", d.Vault().Get)
	mux.HandleFunc("POST /api/vault/forget", d.Vault().Forget)
	mux.HandleFunc("POST /api/vault/snapshot", d.Vault().Snapshot)
	mux.HandleFunc("POST /api/vault/check", d.Vault().Check)
	mux.HandleFunc("POST /api/vault/restore-drill", d.Vault().RestoreDrill)
	mux.HandleFunc("POST /api/vault/prune/allow", d.Vault().AllowPrune)
	mux.HandleFunc("POST /api/vault/migrate-encryption", d.Vault().MigrateEncryption)
	mux.HandleFunc("POST /api/vault/secret-folders/backup", d.Vault().BackupSecretFolders)
	mux.HandleFunc("POST /api/vault/secret-folders/restore", d.Vault().RestoreSecretFolders)
	mux.HandleFunc("POST /api/vault/targets", d.Vault().Targets.Add)
	mux.HandleFunc("PUT /api/vault/targets/{name}", d.Vault().Targets.Change)
	mux.HandleFunc("DELETE /api/vault/targets/{name}", d.Vault().Targets.Remove)
	mux.HandleFunc("POST /api/vault/targets/{name}/snapshot", d.Vault().Targets.Snapshot)
	mux.HandleFunc("POST /api/vault/targets/{name}/check", d.Vault().Targets.Check)
	mux.HandleFunc("POST /api/vault/targets/{name}/restore-drill", d.Vault().Targets.RestoreDrill)
	mux.HandleFunc("POST /api/vault/targets/{name}/prune/allow", d.Vault().Targets.AllowPrune)

	mux.HandleFunc("GET /api/google/client", d.Google().Client)
	mux.HandleFunc("PUT /api/google/client", d.Google().SetClient)
	mux.HandleFunc("DELETE /api/google/client", d.Google().DeleteClient)
	mux.HandleFunc("POST /api/google/client/check", d.Google().CheckClient)
	mux.HandleFunc("POST /api/google/callback", d.Google().PastedCallback)
	mux.HandleFunc("GET /api/calendar/health", d.Google().CalendarHealth)
	mux.HandleFunc("GET /api/brains/{id}/calendar", d.Google().Status)
	mux.HandleFunc("POST /api/brains/{id}/calendar/connect", d.Google().Connect)
	mux.HandleFunc("PUT /api/brains/{id}/calendar", d.Google().Select)
	mux.HandleFunc("DELETE /api/brains/{id}/calendar", d.Google().Disconnect)

	mux.HandleFunc("GET /api/plaud/status", d.Plaud().Status)
	mux.HandleFunc("POST /api/plaud/connect", d.Plaud().Connect)
	mux.HandleFunc("POST /api/plaud/disconnect", d.Plaud().Disconnect)
	mux.HandleFunc("PUT /api/plaud/settings", d.Plaud().Settings)
	mux.HandleFunc("POST /api/plaud/sync", d.Plaud().Sync)
	mux.HandleFunc("POST /api/plaud/resend", d.Plaud().Resend)
	mux.HandleFunc("PUT /api/plaud/app/credentials", d.PlaudApp().Credentials)
	mux.HandleFunc("POST /api/plaud/app/build", d.PlaudApp().Build)
	mux.HandleFunc("GET /api/plaud/app.apk", d.PlaudApp().Download)
	mux.HandleFunc("DELETE /api/plaud/devices/{id}", d.PlaudApp().RevokeDevice)

	mux.HandleFunc("GET /api/github", d.GitHub().Status)
	mux.HandleFunc("POST /api/github/connect", d.GitHub().Connect)
	mux.HandleFunc("POST /api/github/test", d.GitHub().Test)
	mux.HandleFunc("POST /api/github/disconnect", d.GitHub().Disconnect)

	mux.HandleFunc("GET /api/nodes", d.Nodes().List)
	mux.HandleFunc("POST /api/nodes/join-code", d.Nodes().JoinCode)
	mux.HandleFunc("POST /api/nodes/core-secrets-transfer", d.Nodes().CoreSecretsTransfer)
	mux.HandleFunc("POST /api/nodes/move-check", d.Nodes().MoveCheck)
	mux.HandleFunc("POST /api/nodes/move", d.Nodes().StartMove)
	mux.HandleFunc("GET /api/nodes/moves/{id}", d.Nodes().Move)
	mux.HandleFunc("POST /api/nodes/{id}/approve", d.Nodes().ApproveDevice)
	mux.HandleFunc("POST /api/nodes/{id}/approve-commands", d.Nodes().ApproveCommands)
	mux.HandleFunc("POST /api/nodes/{id}/revoke", d.Nodes().Revoke)
	mux.HandleFunc("PUT /api/nodes/{id}/tags", d.Nodes().Tags)
	mux.HandleFunc("POST /api/nodes/{id}/call", d.Nodes().Call)
	mux.HandleFunc("POST /api/nodes/{id}/update", d.Nodes().Update)
	mux.HandleFunc("PUT /api/nodes/{id}/update-policy", d.Nodes().UpdatePolicy)

	d.Site(mux)
	d.Files().Register(mux)
	d.Policy().Register(mux)
	d.Housekeeping().Register(mux)
	d.Schedules().Register(mux)
	d.Connections().Register(mux)
	d.GitIdentities().Register(mux)

	// Only in dev mode: outside it they are not there at all (404).
	if d.DevMode {
		mux.HandleFunc("POST /api/dev/inbound", d.Dev().Inbound)
		mux.HandleFunc("GET /api/dev/outbox", d.Dev().Outbox)
		mux.HandleFunc("POST /api/dev/vote", d.Dev().VoteHandler)
	}

	// No catch-all: the mux answers an unknown path with 404 and a known
	// one asked with the wrong method with 405 and an Allow header.
	return mux
}

func (a *SiteAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/site", a.Status)
	mux.HandleFunc("PUT /api/site/settings", a.Settings)
	mux.HandleFunc("POST /api/site/restart", a.Restart)
	mux.HandleFunc("POST /api/site/deploy-key", a.DeployKey)
	mux.HandleFunc("POST /api/site/deploy-key/github", a.DeployKeyGH)
	mux.HandleFunc("POST /api/site/github-repo", a.CreateGitHubRepo)
	mux.HandleFunc("PUT /api/site/repo", a.Connect)
	mux.HandleFunc("DELETE /api/site/repo", a.Disconnect)
	mux.HandleFunc("POST /api/site/repo/starter", a.Starter)
	mux.HandleFunc("POST /api/site/fetch", a.Fetch)
	mux.HandleFunc("POST /api/site/deploy", a.Deploy)
	mux.HandleFunc("GET /api/site/users", a.Users)
	mux.HandleFunc("POST /api/site/users", a.AddUser)
	mux.HandleFunc("PUT /api/site/users/{id}", a.UpdateUser)
	mux.HandleFunc("DELETE /api/site/users/{id}", a.RemoveUser)
	mux.HandleFunc("PUT /api/site/roles", a.Roles)
	mux.HandleFunc("PUT /api/site/tunnel", a.Tunnel)
	mux.HandleFunc("POST /api/site/check-domain", a.CheckDomain)
	mux.HandleFunc("PUT /api/site/google-client", a.GoogleClient)
	mux.HandleFunc("DELETE /api/site/google-client", a.GoogleClientDelete)
	mux.HandleFunc("GET /api/site/secrets", a.Secrets)
	mux.HandleFunc("PUT /api/site/secrets/{module}/{name}", a.SetSecret)
	mux.HandleFunc("DELETE /api/site/secrets/{module}/{name}", a.RemoveSecret)

	mux.HandleFunc("GET /api/site/builders", a.Builders)
	mux.HandleFunc("PUT /api/site/builders/{id}", a.SetBuilder)
	mux.HandleFunc("GET /api/site/proposals", a.Proposals)
	mux.HandleFunc("POST /api/site/proposals/{id}/merge", a.MergeProposal)
	mux.HandleFunc("DELETE /api/site/proposals/{id}", a.DiscardProposal)
	mux.HandleFunc("POST /api/site/previews/{id}", a.StartPreview)
	a.BuildRoutes(mux)
}

// Register adds the policy's routes: threads, approvals, the audit log,
// the lockdown and the skills' policies.
func (a *PolicyAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/policy", a.get(func(r *http.Request) (any, error) { return a.Status(r.Context()) }))
	mux.HandleFunc("POST /api/policy/lockdown", a.LockdownHandler)
	mux.HandleFunc("GET /api/threads", a.get(func(r *http.Request) (any, error) {
		limit := queryInt(r, "limit")
		return a.Threads(r.Context(), min(max(limit, 0), 500))
	}))
	mux.HandleFunc("GET /api/audit", a.get(a.Audit))
	mux.HandleFunc("GET /api/fride", a.get(func(r *http.Request) (any, error) { return a.Fride(r.Context()) }))
	mux.HandleFunc("POST /api/fride/instances/{name}/stop", a.StopFrideInstanceHandler)
	mux.HandleFunc("POST /api/fride/instances/{name}/start", a.StartFrideInstanceHandler)
	mux.HandleFunc("GET /api/approvals", a.get(func(r *http.Request) (any, error) { return a.Approvals(r.Context()) }))
	mux.HandleFunc("POST /api/approvals/{id}", a.DecideHandler)
	mux.HandleFunc("GET /api/brains/{id}/skills", a.get(func(r *http.Request) (any, error) { return a.Skills(r.Context(), r.PathValue("id")) }))
	mux.HandleFunc("POST /api/brains/{id}/skills/approve", a.ApproveSkillHandler)
	mux.HandleFunc("PUT /api/brains/{id}/skills/policy", a.SkillPolicyHandler)
}

func (a *BuildChatAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/site/build/{id}", a.Chat)
	mux.HandleFunc("POST /api/site/build/{id}", a.Send)
	mux.HandleFunc("POST /api/site/build/{id}/new", a.New)
	mux.HandleFunc("POST /api/site/build/{id}/stop", a.Stop)
}

// brainAPIHandler serves /brain/.
func (a *BrainAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /brain/polls", a.brainCall(a.brainPolls))
	mux.HandleFunc("GET /brain/calendar/calendars", a.brainCall(a.calendarCall(a.brainCalendarList)))
	mux.HandleFunc("GET /brain/calendar/events", a.brainCall(a.calendarCall(a.brainCalendarEvents)))
	mux.HandleFunc("POST /brain/calendar/events", a.brainCall(a.calendarCall(a.brainCalendarCreate)))
	mux.HandleFunc("DELETE /brain/calendar/events/{id}", a.brainCall(a.calendarCall(a.brainCalendarDelete)))
	mux.HandleFunc("POST /brain/calendar/freebusy", a.brainCall(a.calendarCall(a.brainCalendarBusy)))
	mux.HandleFunc("GET /brain/mcp/tools", a.brainCall(a.brainMCPTools))
	mux.HandleFunc("POST /brain/mcp/call", a.brainCall(a.brainMCPCall))
	mux.HandleFunc("GET /brain/memory/search", a.brainCall(a.brainMemorySearch))
	mux.HandleFunc("GET /brain/intents", a.brainCall(a.brainIntents))
	mux.HandleFunc("POST /brain/intents", a.brainCall(a.brainAddIntent))
	mux.HandleFunc("DELETE /brain/intents/{id}", a.brainCall(a.brainRemoveIntent))
	mux.HandleFunc("/brain/", func(w http.ResponseWriter, r *http.Request) {
		WriteErr(w, http.StatusNotFound, "no such brain API call: "+r.Method+" "+r.URL.Path)
	})
	return mux
}

// builderHandler serves /builder/ for the MCP servers of builder brains.
// Each brain's token works only for its own workspace.
func (a *BuilderAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	call := func(fn func(r *http.Request, brainID string) (any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			brainID := a.BrainForToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if brainID == "" {
				WriteErr(w, http.StatusUnauthorized, "this brain may not build the site (Fridolin dashboard → Site → Builders)")
				return
			}
			out, err := fn(r, brainID)
			if err != nil {
				WriteErr(w, http.StatusBadRequest, err.Error())
				return
			}
			WriteJSON(w, http.StatusOK, out)
		}
	}
	mux.HandleFunc("GET /builder/context", call(func(r *http.Request, id string) (any, error) {
		return a.Context(id), nil
	}))
	mux.HandleFunc("POST /builder/check", call(func(r *http.Request, id string) (any, error) {
		return a.Check(r.Context(), id)
	}))
	mux.HandleFunc("POST /builder/preview", call(func(r *http.Request, id string) (any, error) {
		return a.Preview(r.Context(), id)
	}))
	mux.HandleFunc("POST /builder/sync", call(func(r *http.Request, id string) (any, error) {
		out, err := a.Sync(r.Context(), id)
		return BuilderSyncResponse{Result: out}, err
	}))
	mux.HandleFunc("POST /builder/propose", call(func(r *http.Request, id string) (any, error) {
		var req struct{ Title, Summary string }
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64*1024)).Decode(&req); err != nil {
			return nil, err
		}
		p, changes, err := a.Propose(r.Context(), id, req.Title, req.Summary)
		return BuilderProposalResponse{Proposal: p, Changes: changes}, err
	}))
	return mux
}
