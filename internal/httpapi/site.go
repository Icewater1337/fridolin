package httpapi

// Dashboard API for the site (docs/SITE.md). Everything here is behind the
// dashboard token like the rest of /api/; none of it is reachable from the
// site itself.

import (
	"context"
	"encoding/json"
	sitesvc "github.com/Icewater1337/fridolin/internal/site"
	"io"
	"net/http"
	"slices"
	"strings"
)

type SiteService interface {
	AddDeployKeyWithGH(ctx context.Context, addr string) (string, error)
	AddStarter(ctx context.Context) error
	AddUser(in sitesvc.SiteUserInput) (sitesvc.SiteUser, error)
	BaseContext() context.Context
	BuilderPreview(ctx context.Context, brainID string) (map[string]string, error)
	ConnectRepo(ctx context.Context, r sitesvc.SiteRepo) error
	CreateGitHubRepo(ctx context.Context, name string) (string, error)
	Deploy(ctx context.Context, commit string) error
	DeployKey(ctx context.Context) (string, error)
	DeployStatus() *sitesvc.DeploySnapshot
	DiscardProposal(brainID string) error
	DisconnectRepo() error
	Fetch(ctx context.Context) (sitesvc.SiteUpdates, error)
	IsBuilder(brainID string) bool
	MergeProposal(ctx context.Context, brainID string) error
	Proposals(ctx context.Context) []map[string]any
	RemoveSecret(module, name string) error
	RemoveUser(id string) error
	Restart()
	Secrets() ([]sitesvc.SiteModuleSecrets, error)
	SetBuilder(ctx context.Context, brainID string, on bool) error
	SetGoogleClient(data []byte) error
	SetRoles(roles []string) error
	SetSecret(module, name, value string) error
	SetTunnelToken(token string) error
	Settings() sitesvc.SiteSettings
	UpdateSettings(u sitesvc.SiteSettingsUpdate) (sitesvc.SiteSettings, error)
	UpdateUser(id string, in sitesvc.SiteUserInput) (sitesvc.SiteUser, error)
	Users() []sitesvc.SiteUser
}

type SiteAPI struct {
	Site         func() SiteService
	StatusView   func() sitesvc.StatusSnapshot
	BuildersView func() []map[string]any
	BuildRoutes  func(*http.ServeMux)
}

func (a *SiteAPI) Status(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, a.StatusView())
}

func (a *SiteAPI) Settings(w http.ResponseWriter, r *http.Request) {
	var u sitesvc.SiteSettingsUpdate
	if err := DecodeJSON(r, &u); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if u.Enabled != nil && *u.Enabled {
		users := a.Site().Users()
		if !slices.ContainsFunc(users, func(x sitesvc.SiteUser) bool { return x.Role == "admin" }) {
			WriteErr(w, http.StatusBadRequest, "add an admin account (your Google address) before turning the site on")
			return
		}
		if a.Site().Settings().Approved == "" {
			WriteErr(w, http.StatusBadRequest, "deploy a commit of the site repository first")
			return
		}
	}
	s, err := a.Site().UpdateSettings(u)
	if err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, s)
}

func (a *SiteAPI) Restart(w http.ResponseWriter, r *http.Request) {
	go a.Site().Restart()
	WriteJSON(w, http.StatusAccepted, OKResponse{Ok: true})
}

func (a *SiteAPI) DeployKey(w http.ResponseWriter, r *http.Request) {
	pub, err := a.Site().DeployKey(r.Context())
	if err != nil {
		WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, DeployKeyResponse{DeployKey: pub})
}

// apiSiteDeployKeyGH adds the deploy key with gh. Before a repository is
// connected the page sends the address typed for Connect as "url"; once one
// is, it sends no body.
func (a *SiteAPI) DeployKeyGH(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := DecodeOptionalJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	repo, err := a.Site().AddDeployKeyWithGH(r.Context(), req.URL)
	if err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, DeployKeyAddedResponse{Ok: true, Repo: repo})
}

func (a *SiteAPI) CreateGitHubRepo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := a.Site().CreateGitHubRepo(r.Context(), strings.TrimSpace(req.Name))
	if err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, RepositoryCreatedResponse{URL: u})
}

func (a *SiteAPI) Connect(w http.ResponseWriter, r *http.Request) {
	var req sitesvc.SiteRepo
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.Site().ConnectRepo(r.Context(), req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, a.Site().Settings())
}

func (a *SiteAPI) Disconnect(w http.ResponseWriter, r *http.Request) {
	if err := a.Site().DisconnectRepo(); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OKResponse{Ok: true})
}

func (a *SiteAPI) Starter(w http.ResponseWriter, r *http.Request) {
	if err := a.Site().AddStarter(r.Context()); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OKResponse{Ok: true})
}

func (a *SiteAPI) Fetch(w http.ResponseWriter, r *http.Request) {
	up, err := a.Site().Fetch(r.Context())
	if err != nil {
		WriteErr(w, http.StatusBadGateway, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, up)
}

func (a *SiteAPI) Deploy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Commit string `json:"commit"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// The deploy outlives this request.
	if err := a.Site().Deploy(a.Site().BaseContext(), req.Commit); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusAccepted, a.Site().DeployStatus())
}

func (a *SiteAPI) Users(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, SiteUsersResponse{Users: a.Site().Users(), Roles: a.Site().Settings().Roles})
}

func (a *SiteAPI) AddUser(w http.ResponseWriter, r *http.Request) {
	var in sitesvc.SiteUserInput
	if err := DecodeJSON(r, &in); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := a.Site().AddUser(in)
	if err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, u)
}

func (a *SiteAPI) UpdateUser(w http.ResponseWriter, r *http.Request) {
	var in sitesvc.SiteUserInput
	if err := DecodeJSON(r, &in); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := a.Site().UpdateUser(r.PathValue("id"), in)
	if err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, u)
}

func (a *SiteAPI) RemoveUser(w http.ResponseWriter, r *http.Request) {
	if err := a.Site().RemoveUser(r.PathValue("id")); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OKResponse{Ok: true})
}

func (a *SiteAPI) Roles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Roles []string `json:"roles"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.Site().SetRoles(req.Roles); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, SiteRolesResponse{Roles: a.Site().Settings().Roles})
}

func (a *SiteAPI) Tunnel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.Site().SetTunnelToken(req.Token); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OKResponse{Ok: true})
}

// apiSiteGoogleClient saves the site's own Google client and restarts the
// site. Its runtime.json names the client file it reads, chosen when it
// starts (googleClientFile): without a restart it keeps the shared client.
func (a *SiteAPI) GoogleClient(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Accept the downloaded file as is, or wrapped as {"json": "..."}.
	var wrapped struct {
		JSON string `json:"json"`
	}
	if json.Unmarshal(data, &wrapped) == nil && wrapped.JSON != "" {
		data = []byte(wrapped.JSON)
	}
	if err := a.Site().SetGoogleClient(data); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	go a.Site().Restart()
	WriteJSON(w, http.StatusOK, OKResponse{Ok: true})
}

// apiSiteGoogleClientDelete removes the site's own Google client and restarts
// the site, which then uses Fridolin's shared one.
func (a *SiteAPI) GoogleClientDelete(w http.ResponseWriter, r *http.Request) {
	if err := a.Site().SetGoogleClient(nil); err != nil {
		WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	go a.Site().Restart()
	WriteJSON(w, http.StatusOK, OKResponse{Ok: true})
}

func (a *SiteAPI) Builders(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, BuildersResponse{Brains: a.BuildersView()})
}

func (a *SiteAPI) SetBuilder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On bool `json:"on"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Setting up the workspace can take a while (npm install); it outlives the request.
	if err := a.Site().SetBuilder(a.Site().BaseContext(), r.PathValue("id"), req.On); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OKResponse{Ok: true})
}

func (a *SiteAPI) Proposals(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, SiteProposalsResponse{Proposals: a.Site().Proposals(r.Context())})
}

func (a *SiteAPI) MergeProposal(w http.ResponseWriter, r *http.Request) {
	if err := a.Site().MergeProposal(r.Context(), r.PathValue("id")); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusAccepted, a.Site().DeployStatus())
}

func (a *SiteAPI) DiscardProposal(w http.ResponseWriter, r *http.Request) {
	if err := a.Site().DiscardProposal(r.PathValue("id")); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OKResponse{Ok: true})
}

func (a *SiteAPI) StartPreview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !a.Site().IsBuilder(id) {
		WriteErr(w, http.StatusBadRequest, "that brain does not build the site")
		return
	}
	urls, err := a.Site().BuilderPreview(a.Site().BaseContext(), id)
	if err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, urls)
}

// apiSiteSecrets lists the modules' secrets by name. Values never leave the
// bridge except to the live site.
func (a *SiteAPI) Secrets(w http.ResponseWriter, r *http.Request) {
	list, err := a.Site().Secrets()
	if err != nil {
		WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, SiteSecretsResponse{Modules: list})
}

// apiSiteSetSecret saves a secret and restarts the site, which reads its
// secrets only when it starts.
func (a *SiteAPI) SetSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Value string `json:"value"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.Site().SetSecret(r.PathValue("module"), r.PathValue("name"), req.Value); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	go a.Site().Restart()
	WriteJSON(w, http.StatusOK, OKResponse{Ok: true})
}

func (a *SiteAPI) RemoveSecret(w http.ResponseWriter, r *http.Request) {
	if err := a.Site().RemoveSecret(r.PathValue("module"), r.PathValue("name")); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	go a.Site().Restart()
	WriteJSON(w, http.StatusOK, OKResponse{Ok: true})
}
