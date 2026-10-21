package backup

import "context"

// connectJob is a sign-in that runs in the background while the dashboard
// polls for it: Google Drive's for backups. It waits for an approval in
// the browser, which can take minutes, and then sets up the Drive folder.
type connectJob struct {
	State string `json:"state"` // running, done, error
	// Step is what a running sign-in waits for: signin (Google's consent
	// page, at AuthURL) or folder (the Drive folder being set up).
	Step    string `json:"step,omitempty"`
	AuthURL string `json:"auth_url,omitempty"`
	Error   string `json:"error,omitempty"`
	// Reconnect is set for a switch of a working link to another client:
	// the old link keeps working until the new one is in place.
	Reconnect bool `json:"reconnect,omitempty"`
	cancel    context.CancelFunc
	// done, when set, is closed once the sign-in's goroutine has ended, so
	// that whoever cancels it can wait for what it still writes.
	done chan struct{}
}

// The steps of a running connectJob.
const (
	stepSignIn = "signin"
	stepFolder = "folder"
)
