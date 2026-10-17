package main

import "context"

// connectJob is a sign-in that runs in the background while the dashboard
// polls for it: Google Drive's for backups, Plaud's for recordings. Both
// wait for an approval in the browser, which can take minutes.
type connectJob struct {
	State   string `json:"state"` // running, done, error
	AuthURL string `json:"auth_url,omitempty"`
	Error   string `json:"error,omitempty"`
	cancel  context.CancelFunc
}
