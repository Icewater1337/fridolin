package httpapi

import (
	"context"
	"github.com/Icewater1337/fridolin/internal/backup"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/registry"
	"net/http"
	"time"
)

type StatusResponse struct {
	Version       string            `json:"version"`
	StartedAt     time.Time         `json:"started_at"`
	UptimeS       int64             `json:"uptime_s"`
	ConfigPath    string            `json:"config_path"`
	DataDir       string            `json:"data_dir"`
	DevMode       bool              `json:"dev_mode"`
	Platform      string            `json:"platform"`
	WhatsApp      WhatsAppStatus    `json:"whatsapp"`
	Claude        claudecli.Status  `json:"claude"`
	AgentProvider string            `json:"agent_provider"`
	Codex         *claudecli.Status `json:"codex,omitempty"`
	Sandbox       SandboxStatus     `json:"sandbox"`
	Brains        []brain.Runtime   `json:"brains"`
	Issues        []registry.Issue  `json:"issues"`
	Warnings      []string          `json:"warnings"`
	Backup        backup.Summary    `json:"backup"`
	Ingest        bool              `json:"ingest"`
	Pending       int               `json:"debouncing"`
	// Settings saved since the start that only apply after a restart. The
	// dashboard shows its restart banner from this, in every tab.
	RestartRequired []string `json:"restart_required"`
}

type WhatsAppStatus struct {
	chat.LinkState
	QRAvailable bool `json:"qr_available"`
}

type SandboxStatus struct {
	Enabled   bool `json:"enabled"`
	Supported bool `json:"supported"`
}

type StatusAPI struct {
	Snapshot func(context.Context) StatusResponse
}

func (a StatusAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, a.Snapshot(r.Context()))
}
