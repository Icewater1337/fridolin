package site

import (
	"github.com/Icewater1337/fridolin/internal/fsx"
	"os"
	"strings"
)

func (m *SiteManager) StatusView() map[string]any {
	s := m.Settings()
	resp := map[string]any{"settings": s}

	node, nodeErr := m.host.Node()
	_, cfErr := m.host.Tool("cloudflared")
	_, ghErr := m.host.Tool("gh")
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
		"sandbox":   m.SandboxStatus(),
	}
	if s.Domain != "" {
		resp["public_url"] = "https://" + s.Domain + "/"
	}
	resp["tunnel"] = map[string]any{"process": m.tunnel.status(), "token_saved": fsx.Exists(m.dir("tunnel.token"))}

	repo := map[string]any{"connected": fsx.Exists(m.repoDir()), "github": githubRepo(s.Repo.URL)}
	resp["repo"] = repo
	if pub, err := os.ReadFile(m.dir("deploy_key.pub")); err == nil {
		resp["deploy_key"] = strings.TrimSpace(string(pub))
	}

	googleStatus := map[string]any{"source": "none"}
	if p := m.googleClientFile(); p != "" {
		if data, err := os.ReadFile(p); err == nil {
			if clientType, err := m.host.GoogleClientType(data); err == nil {
				googleStatus["type"] = clientType
				googleStatus["source"] = "shared"
				if p == m.dir("google_client.json") {
					googleStatus["source"] = "site"
				}
			}
		}
	}
	origin := "http://" + s.Listen
	if s.Domain != "" {
		origin = "https://" + s.Domain
	}
	googleStatus["redirect_uri"] = origin + "/auth/callback"
	resp["google"] = googleStatus

	users := m.Users()
	admins := 0
	for _, u := range users {
		if u.Role == "admin" {
			admins++
		}
	}
	resp["accounts"] = map[string]int{"total": len(users), "admins": admins}
	resp["deploy"] = m.DeployStatus()
	return resp
}
