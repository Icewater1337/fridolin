package httpapi

import (
	config "github.com/Icewater1337/fridolin/internal/config"
	google "github.com/Icewater1337/fridolin/internal/google"
	site "github.com/Icewater1337/fridolin/internal/site"
	time "time"
)

type DirectoryEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsBrain bool   `json:"is_brain"`
	IsGit   bool   `json:"is_git"`
}

// Response fields keep the original map encoder order and required keys.
type AuthURLResponse struct {
	AuthURL string `json:"auth_url"`
}

type BackupVersionsResponse struct {
	Brain    string   `json:"brain"`
	Versions []string `json:"versions"`
}

type BrainCreatedResponse struct {
	Created []string `json:"created"`
	ID      string   `json:"id"`
	Path    string   `json:"path"`
}

type BuilderProposalResponse struct {
	Changes  any `json:"changes"`
	Proposal any `json:"proposal"`
}

type BuilderSyncResponse struct {
	Result string `json:"result"`
}

type BuildersResponse struct {
	Brains []map[string]any `json:"brains"`
}

type CalendarBusyResponse struct {
	Busy      []google.BusySlot `json:"busy"`
	Calendars []string          `json:"calendars"`
}

type CalendarCreatedResponse struct {
	Created google.CalendarEvent `json:"created"`
}

type CalendarDeletedResponse struct {
	Deleted string `json:"deleted"`
}

type CalendarDisconnectedResponse struct {
	Note    string `json:"note"`
	Revoked bool   `json:"revoked"`
	Status  string `json:"status"`
}

type CalendarListResponse struct {
	Calendars  []CalendarChoice `json:"calendars"`
	Restricted bool             `json:"restricted"`
}

type ChatAllowedResponse struct {
	Entry  string `json:"entry"`
	Status string `json:"status"`
}

type ChatMembersResponse struct {
	Members []string `json:"members"`
	Status  string   `json:"status"`
}

type ChatRevokedResponse struct {
	Removed []string `json:"removed"`
	Status  string   `json:"status"`
}

type ConfigResponse struct {
	Config    *config.Config `json:"config"`
	Effective *config.Config `json:"effective"`
	ETag      string         `json:"etag"`
	Path      string         `json:"path"`
}

type ConfigSavedResponse struct {
	ETag            string   `json:"etag"`
	RestartRequired []string `json:"restart_required"`
}

type DeployKeyAddedResponse struct {
	Ok   bool   `json:"ok"`
	Repo string `json:"repo"`
}

type DeployKeyResponse struct {
	DeployKey string `json:"deploy_key"`
}

type DevIgnoredResponse struct {
	Reason string `json:"reason"`
	Status string `json:"status"`
}

type DirectoryResponse struct {
	Entries []DirectoryEntry `json:"entries"`
	Home    string           `json:"home"`
	Parent  string           `json:"parent"`
	Path    string           `json:"path"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type GoogleClientSavedResponse struct {
	Note   string `json:"note"`
	Status string `json:"status"`
	Type   string `json:"type"`
}

type IngestAcceptedResponse struct {
	JID    string `json:"jid"`
	Status string `json:"status"`
}

type LoginCodeResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

type OKResponse struct {
	Ok bool `json:"ok"`
}

type OperationResponse struct {
	Status string `json:"status"`
}

type PickerCancelledResponse struct {
	Cancelled bool `json:"cancelled"`
}

type PickerCapabilitiesResponse struct {
	Home   string `json:"home"`
	Picker string `json:"picker"`
}

type PickerPathResponse struct {
	Path string `json:"path"`
}

type PollsResponse struct {
	Count int `json:"count"`
	Polls any `json:"polls"`
}

type QRCodeResponse struct {
	CreatedAt time.Time `json:"created_at"`
	DataURL   string    `json:"data_url"`
}

type RepositoryCreatedResponse struct {
	URL string `json:"url"`
}

type RestoreStartedResponse struct {
	Status string `json:"status"`
	Target string `json:"target"`
}

type RunStoppedResponse struct {
	Stopped bool `json:"stopped"`
}

type SignInResponse struct {
	Binding string `json:"binding"`
	Status  string `json:"status"`
}

type SiteProposalsResponse struct {
	Proposals []map[string]any `json:"proposals"`
}

type SiteRolesResponse struct {
	Roles []string `json:"roles"`
}

type SiteSecretsResponse struct {
	Modules []site.SiteModuleSecrets `json:"modules"`
}

type SiteUsersResponse struct {
	Roles []string        `json:"roles"`
	Users []site.SiteUser `json:"users"`
}
