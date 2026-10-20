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

// APIError is the bridge refusing a dashboard API call, with its status
// and the error it gave.
type APIError struct {
	Status  int
	Message string
	// Code is the node protocol's error code of a refused node call.
	Code string
}

func (e *APIError) Error() string { return e.Message }

// API calls the running bridge's dashboard API as the dashboard does, with
// the token from the data dir: method on path, body (nil for none) as
// JSON, and the JSON answer decoded into out (nil to drop it).
func API(cfg *config.Settings, method, path string, body, out any) error {
	if !cfg.HTTPEnabled {
		return errors.New("the bridge's HTTP server is off (http_listen), so it cannot be asked")
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
	if method != http.MethodGet {
		req.Header.Set("X-Bridge-Dashboard", "1")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("%w at http://%s — start it first", ErrBridgeDown, cfg.HTTPListen)
	}
	if err != nil {
		return fmt.Errorf("asking the bridge at http://%s: %w", cfg.HTTPListen, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound && !json.Valid(data) {
		return fmt.Errorf("the bridge at http://%s %w (it has no %s %s)", cfg.HTTPListen, ErrOlderBridge, method, path)
	}
	if resp.StatusCode/100 != 2 {
		var e struct{ Error, Code string }
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return &APIError{Status: resp.StatusCode, Message: e.Error, Code: e.Code}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}
