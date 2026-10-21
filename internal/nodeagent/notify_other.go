//go:build !windows

package nodeagent

import (
	"context"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// windowsToast is only called on Windows.
func windowsToast(_ context.Context, title, msg string, logf func(string, ...any)) (nodeproto.NotifyResult, error) {
	logf("node: notification: %s: %s", title, msg)
	return nodeproto.NotifyResult{Method: "log", Reason: "toasts are shown on Windows only"}, nil
}
