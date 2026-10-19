package controlcli

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// DashboardLink is the address `fridolin open` opens for cfg's bridge. It
// carries a code that works once, within a minute, not the token: a
// browser keeps every address it opened. A bridge older than this binary
// has no login codes, and its dashboard took the token in the link; there
// is no falling back to that, since `fridolin open --print` is what an
// agent runs and pastes into a chat.
func DashboardLink(cfg *config.Settings) (string, error) {
	tokenPath := filepath.Join(cfg.DataDir, "dashboard.token")
	token, err := httpapi.ReadToken(cfg.DataDir)
	if err != nil {
		return "", fmt.Errorf("no dashboard token yet — start the bridge first (%w)", err)
	}
	code, err := AskLoginCode(cfg.HTTPListen, token)
	if errors.Is(err, ErrOlderBridge) {
		return "", fmt.Errorf("%w. Restart it on this build (./setup.sh installs it and restarts the bridge), or open its dashboard with the fridolin that runs it, or at http://%s/ with the token from %s", err, cfg.HTTPListen, tokenPath)
	}
	if err != nil {
		return "", err
	}
	return httpapi.LoginURL(cfg.HTTPListen, code), nil
}

// RotateToken does rotateTokenCmd's work for cfg.
func RotateToken(cfg *config.Settings) error {
	token, err := httpapi.ReadToken(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("no dashboard token yet — start the bridge first (%w)", err)
	}
	if cfg.HTTPEnabled {
		var resp struct{ Status string }
		err := callBridge(cfg.HTTPListen, token, "/api/token/rotate", &resp)
		if errors.Is(err, ErrOlderBridge) && cfg.Dashboard.Disabled {
			// It serves no dashboard API, so it answers 404 whatever its
			// build. Nothing takes the token until the dashboard is on.
			return fmt.Errorf("the dashboard is turned off (dashboard.disabled), so the bridge at http://%s cannot replace the token while it runs. Run fridolin rotate-token while the bridge is stopped, or once the dashboard is on and the bridge restarted", cfg.HTTPListen)
		}
		if errors.Is(err, ErrOlderBridge) {
			// It keeps the token in memory and has no way to replace it.
			return fmt.Errorf("%w, so the token was not replaced. Restart it on this build (./setup.sh installs it and restarts the bridge), then run fridolin rotate-token again", err)
		}
		if !errors.Is(err, ErrBridgeDown) {
			return err
		}
	}
	// No bridge answers. The instance lock says whether one runs anyway
	// (with its server off, say); only without one are the files changed
	// here, since a running bridge keeps the token in memory.
	lock, err := platform.AcquireLock(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("the bridge runs but does not answer, so the token was not replaced: %w", err)
	}
	defer lock.Release()
	tok, err := httpapi.LoadDashboardToken(cfg.DataDir)
	if err != nil {
		return err
	}
	if err := tok.Rotate(); err != nil {
		return fmt.Errorf("writing a new token: %w", err)
	}
	sessions, err := httpapi.LoadDashboardSessions(cfg.DataDir, time.Now)
	if err != nil {
		return err
	}
	return sessions.DropAll()
}
