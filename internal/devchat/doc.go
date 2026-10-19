// Package devchat records simulated outgoing chat messages in a bounded,
// synchronized outbox for the dashboard's dev mode. The caller supplies the
// current chunking policy; no configuration or network client is read here.
package devchat
