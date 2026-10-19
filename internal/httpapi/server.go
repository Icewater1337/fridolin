package httpapi

import (
	"net/http"
	"time"
)

// NewServer is the server for the dashboard, /send, /ingest and
// /brain/. Any local program can connect, so none may hold a connection
// for ever: reading a request (headers and body, which is 1 MiB at most)
// is cut off after 30 seconds and an idle keep-alive connection after two
// minutes. There is no write timeout, because some handlers rightly take
// minutes: a folder picker waits for you, rclone lists versions, a sign-in
// checks back with Google.
func NewServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
}
