package controlcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"syscall"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

// VaultCall sends one request of fridolin vault to the running bridge's
// /api/vault/ routes, with the dashboard token, and decodes the answer
// into out (raw JSON when out is a *json.RawMessage). timeout bounds the
// whole call: bringing a large file back from the cold tier takes long.
func VaultCall(cfg *config.Settings, method, path string, body, out any, timeout time.Duration) error {
	return DashboardCall(cfg, "fridolin vault", method, path, body, out, timeout)
}

// DashboardCall sends one request of command (such as fridolin
// housekeeping) to the running bridge's dashboard API, as VaultCall does.
func DashboardCall(cfg *config.Settings, command, method, path string, body, out any, timeout time.Duration) error {
	if !cfg.HTTPEnabled || cfg.Dashboard.Disabled {
		return errors.New("the bridge serves no dashboard API (http_listen is off or dashboard.disabled), which " + command + " talks to")
	}
	token, err := httpapi.ReadToken(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("no dashboard token yet — start the bridge first (%w)", err)
	}
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, "http://"+cfg.HTTPListen+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-Bridge-Token", token)
	req.Header.Set("X-Bridge-Dashboard", "1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("%w at http://%s", ErrBridgeDown, cfg.HTTPListen)
	}
	if err != nil {
		return fmt.Errorf("asking the bridge at http://%s: %w", cfg.HTTPListen, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusNotFound && !json.Valid(data) {
		return fmt.Errorf("the bridge at http://%s %w (it has no %s %s)", cfg.HTTPListen, ErrOlderBridge, method, path)
	}
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("the bridge refused: %s", e.Error)
	}
	if raw, ok := out.(*json.RawMessage); ok {
		*raw = append((*raw)[:0], data...)
		return nil
	}
	return json.Unmarshal(data, out)
}
