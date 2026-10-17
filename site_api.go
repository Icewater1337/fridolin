package main

// Dashboard API for the site (docs/SITE.md). Everything here is behind the
// dashboard token like the rest of /api/; none of it is reachable from the
// site itself.

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
)

func (b *Bridge) siteAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/site", b.apiSiteStatus)
	mux.HandleFunc("PUT /api/site/settings", b.apiSiteSettings)
	mux.HandleFunc("POST /api/site/restart", b.apiSiteRestart)
	mux.HandleFunc("POST /api/site/deploy-key", b.apiSiteDeployKey)
	mux.HandleFunc("POST /api/site/deploy-key/github", b.apiSiteDeployKeyGH)
	mux.HandleFunc("POST /api/site/github-repo", b.apiSiteCreateGitHubRepo)
	mux.HandleFunc("PUT /api/site/repo", b.apiSiteConnect)
	mux.HandleFunc("DELETE /api/site/repo", b.apiSiteDisconnect)
	mux.HandleFunc("POST /api/site/repo/starter", b.apiSiteStarter)
	mux.HandleFunc("POST /api/site/fetch", b.apiSiteFetch)
	mux.HandleFunc("POST /api/site/deploy", b.apiSiteDeploy)
	mux.HandleFunc("GET /api/site/users", b.apiSiteUsers)
	mux.HandleFunc("POST /api/site/users", b.apiSiteAddUser)
	mux.HandleFunc("PUT /api/site/users/{id}", b.apiSiteUpdateUser)
	mux.HandleFunc("DELETE /api/site/users/{id}", b.apiSiteRemoveUser)
	mux.HandleFunc("PUT /api/site/roles", b.apiSiteRoles)
	mux.HandleFunc("PUT /api/site/tunnel", b.apiSiteTunnel)
	mux.HandleFunc("PUT /api/site/google-client", b.apiSiteGoogleClient)
	mux.HandleFunc("DELETE /api/site/google-client", b.apiSiteGoogleClientDelete)
}

func (b *Bridge) apiSiteStatus(w http.ResponseWriter, r *http.Request) {
	m := b.site
	s := m.Settings()
	resp := map[string]any{"settings": s}

	node, nodeErr := findNode()
	_, cfErr := findTool("cloudflared")
	_, ghErr := findTool("gh")
	rt := map[string]any{"node": node, "cloudflared": cfErr == nil, "gh": ghErr == nil}
	if nodeErr != nil {
		rt["node_error"] = nodeErr.Error()
	}
	resp["runtime"] = rt

	site := m.site.status()
	resp["site"] = map[string]any{
		"process":   site,
		"healthy":   site.State == "running" && siteHealthy(s.Listen),
		"local_url": "http://" + s.Listen + "/",
	}
	if s.Domain != "" {
		resp["public_url"] = "https://" + s.Domain + "/"
	}
	resp["tunnel"] = map[string]any{"process": m.tunnel.status(), "token_saved": fileExists(m.dir("tunnel.token"))}

	repo := map[string]any{"connected": fileExists(m.repoDir()), "github": githubRepo(s.Repo.URL)}
	resp["repo"] = repo
	if pub, err := os.ReadFile(m.dir("deploy_key.pub")); err == nil {
		resp["deploy_key"] = strings.TrimSpace(string(pub))
	}

	google := map[string]any{"source": "none"}
	if p := m.googleClientFile(); p != "" {
		if data, err := os.ReadFile(p); err == nil {
			if c, err := ParseGoogleClient(data); err == nil {
				google["type"] = c.Type
				google["source"] = "shared"
				if p == m.dir("google_client.json") {
					google["source"] = "site"
				}
			}
		}
	}
	origin := "http://" + s.Listen
	if s.Domain != "" {
		origin = "https://" + s.Domain
	}
	google["redirect_uri"] = origin + "/auth/callback"
	resp["google"] = google

	users := m.Users()
	admins := 0
	for _, u := range users {
		if u.Role == "admin" {
			admins++
		}
	}
	resp["accounts"] = map[string]int{"total": len(users), "admins": admins}
	resp["deploy"] = m.DeployStatus()
	writeJSON(w, http.StatusOK, resp)
}

func (b *Bridge) apiSiteSettings(w http.ResponseWriter, r *http.Request) {
	var u SiteSettingsUpdate
	if err := decodeJSON(r, &u); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if u.Enabled != nil && *u.Enabled {
		users := b.site.Users()
		if !slices.ContainsFunc(users, func(x SiteUser) bool { return x.Role == "admin" }) {
			writeErr(w, http.StatusBadRequest, "add an admin account (your Google address) before turning the site on")
			return
		}
		if b.site.Settings().Approved == "" {
			writeErr(w, http.StatusBadRequest, "deploy a commit of the site repository first")
			return
		}
	}
	s, err := b.site.UpdateSettings(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (b *Bridge) apiSiteRestart(w http.ResponseWriter, r *http.Request) {
	go b.site.Restart()
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (b *Bridge) apiSiteDeployKey(w http.ResponseWriter, r *http.Request) {
	pub, err := b.site.DeployKey(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deploy_key": pub})
}

func (b *Bridge) apiSiteDeployKeyGH(w http.ResponseWriter, r *http.Request) {
	if err := b.site.AddDeployKeyWithGH(r.Context()); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (b *Bridge) apiSiteCreateGitHubRepo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := b.site.CreateGitHubRepo(r.Context(), strings.TrimSpace(req.Name))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": u})
}

func (b *Bridge) apiSiteConnect(w http.ResponseWriter, r *http.Request) {
	var req SiteRepo
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := b.site.ConnectRepo(r.Context(), req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, b.site.Settings())
}

func (b *Bridge) apiSiteDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := b.site.DisconnectRepo(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (b *Bridge) apiSiteStarter(w http.ResponseWriter, r *http.Request) {
	if err := b.site.AddStarter(r.Context()); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (b *Bridge) apiSiteFetch(w http.ResponseWriter, r *http.Request) {
	up, err := b.site.Fetch(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, up)
}

func (b *Bridge) apiSiteDeploy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Commit string `json:"commit"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// The deploy outlives this request.
	if err := b.site.Deploy(b.site.baseContext(), req.Commit); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, b.site.DeployStatus())
}

func (b *Bridge) apiSiteUsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"users": b.site.Users(), "roles": b.site.Settings().Roles})
}

func (b *Bridge) apiSiteAddUser(w http.ResponseWriter, r *http.Request) {
	var in SiteUserInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := b.site.AddUser(in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (b *Bridge) apiSiteUpdateUser(w http.ResponseWriter, r *http.Request) {
	var in SiteUserInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := b.site.UpdateUser(r.PathValue("id"), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (b *Bridge) apiSiteRemoveUser(w http.ResponseWriter, r *http.Request) {
	if err := b.site.RemoveUser(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (b *Bridge) apiSiteRoles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Roles []string `json:"roles"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := b.site.SetRoles(req.Roles); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": b.site.Settings().Roles})
}

func (b *Bridge) apiSiteTunnel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := b.site.SetTunnelToken(req.Token); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (b *Bridge) apiSiteGoogleClient(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Accept the downloaded file as is, or wrapped as {"json": "..."}.
	var wrapped struct {
		JSON string `json:"json"`
	}
	if json.Unmarshal(data, &wrapped) == nil && wrapped.JSON != "" {
		data = []byte(wrapped.JSON)
	}
	if err := b.site.SetGoogleClient(data); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (b *Bridge) apiSiteGoogleClientDelete(w http.ResponseWriter, r *http.Request) {
	if err := b.site.SetGoogleClient(nil); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
