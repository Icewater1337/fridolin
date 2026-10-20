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
	// Core is the containerized core's tailnet node and lease, when either
	// is on (core.tailnet, core.lease).
	Core *CoreStatus `json:"core,omitempty"`
}

// CoreStatus is what the Overview shows of the core.
type CoreStatus struct {
	Tailnet *TailnetStatus `json:"tailnet,omitempty"`
	Lease   *LeaseStatus   `json:"lease,omitempty"`
}

// TailnetStatus is the core's tailnet node: State is starting, up or
// error; URL is where the dashboard answers on the tailnet.
type TailnetStatus struct {
	State    string   `json:"state"`
	Hostname string   `json:"hostname"`
	DNSName  string   `json:"dns_name,omitempty"`
	IPs      []string `json:"ips,omitempty"`
	MagicDNS bool     `json:"magic_dns"`
	HTTPS    bool     `json:"https"`
	URL      string   `json:"url,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// LeaseStatus is the core lease as this bridge sees it: State is held
// (this device renews it) or lost (the channels are stopped); Holder is
// the device that took it, when another did.
type LeaseStatus struct {
	State     string     `json:"state"`
	DeviceID  string     `json:"device_id"`
	Hostname  string     `json:"hostname,omitempty"`
	RenewedAt *time.Time `json:"renewed_at,omitempty"`
	Holder    string     `json:"holder,omitempty"`
	Error     string     `json:"error,omitempty"`
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
