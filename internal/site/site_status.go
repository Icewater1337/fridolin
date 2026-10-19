package site

import (
	"github.com/Icewater1337/fridolin/internal/fsx"
	"os"
	"strings"
)

type StatusSnapshot struct {
	Accounts  AccountCounts    `json:"accounts"`
	Deploy    *DeploySnapshot  `json:"deploy"`
	DeployKey *string          `json:"deploy_key,omitempty"`
	Google    GoogleStatus     `json:"google"`
	PublicURL string           `json:"public_url,omitempty"`
	Repo      RepositoryStatus `json:"repo"`
	Runtime   RuntimeStatus    `json:"runtime"`
	Settings  SiteSettings     `json:"settings"`
	Site      ProcessStatus    `json:"site"`
	Tunnel    TunnelStatus     `json:"tunnel"`
}
type AccountCounts struct {
	Admins int `json:"admins"`
	Total  int `json:"total"`
}
type GoogleStatus struct {
	RedirectURI string  `json:"redirect_uri"`
	Source      string  `json:"source"`
	Type        *string `json:"type,omitempty"`
}
type RepositoryStatus struct {
	Connected bool   `json:"connected"`
	GitHub    string `json:"github"`
}
type RuntimeStatus struct {
	Cloudflared bool    `json:"cloudflared"`
	GH          bool    `json:"gh"`
	Node        string  `json:"node"`
	NodeError   *string `json:"node_error,omitempty"`
}
type ProcessStatus struct {
	Healthy  bool              `json:"healthy"`
	LocalURL string            `json:"local_url"`
	Process  SiteProcStatus    `json:"process"`
	Sandbox  SiteSandboxStatus `json:"sandbox"`
}
type TunnelStatus struct {
	Process    SiteProcStatus `json:"process"`
	TokenSaved bool           `json:"token_saved"`
}

func (m *SiteManager) StatusView() StatusSnapshot {
	s := m.Settings()
	resp := StatusSnapshot{Settings: s}

	node, nodeErr := m.host.Node()
	_, cfErr := m.host.Tool("cloudflared")
	_, ghErr := m.host.Tool("gh")
	rt := RuntimeStatus{Node: node, Cloudflared: cfErr == nil, GH: ghErr == nil}
	if nodeErr != nil {
		message := nodeErr.Error()
		rt.NodeError = &message
	}
	resp.Runtime = rt

	site := m.site.status()
	resp.Site = ProcessStatus{Process: site, Healthy: site.State == "running" && siteHealthy(s.Listen), LocalURL: "http://" + s.Listen + "/", Sandbox: m.SandboxStatus()}
	if s.Domain != "" {
		resp.PublicURL = "https://" + s.Domain + "/"
	}
	resp.Tunnel = TunnelStatus{Process: m.tunnel.status(), TokenSaved: fsx.Exists(m.dir("tunnel.token"))}

	repo := RepositoryStatus{Connected: fsx.Exists(m.repoDir()), GitHub: githubRepo(s.Repo.URL)}
	resp.Repo = repo
	if pub, err := os.ReadFile(m.dir("deploy_key.pub")); err == nil {
		key := strings.TrimSpace(string(pub))
		resp.DeployKey = &key
	}

	googleStatus := GoogleStatus{Source: "none"}
	if p := m.googleClientFile(); p != "" {
		if data, err := os.ReadFile(p); err == nil {
			if clientType, err := m.host.GoogleClientType(data); err == nil {
				googleStatus.Type = &clientType
				googleStatus.Source = "shared"
				if p == m.dir("google_client.json") {
					googleStatus.Source = "site"
				}
			}
		}
	}
	origin := "http://" + s.Listen
	if s.Domain != "" {
		origin = "https://" + s.Domain
	}
	googleStatus.RedirectURI = origin + "/auth/callback"
	resp.Google = googleStatus

	users := m.Users()
	admins := 0
	for _, u := range users {
		if u.Role == "admin" {
			admins++
		}
	}
	resp.Accounts = AccountCounts{Total: len(users), Admins: admins}
	resp.Deploy = m.DeployStatus()
	return resp
}
