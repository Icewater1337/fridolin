package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"syscall"
	"time"
)

// The commands that ask the running bridge for something, `fridolin open`
// and `fridolin rotate-token`, do it through its dashboard API, with the
// token from the data dir.

// errOlderBridge is a bridge that answers 404 for an endpoint this binary
// asks for: one that runs an older build, such as the bridge setup.sh
// leaves running when its install is declined.
var errOlderBridge = errors.New("runs a build older than this fridolin")

// callBridge sends a dashboard API write to the bridge at listen, with the
// token, and decodes its JSON answer into out.
func callBridge(listen, token, path string, out any) error {
	req, err := http.NewRequest(http.MethodPost, "http://"+listen+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Bridge-Token", token)
	req.Header.Set("X-Bridge-Dashboard", "1")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("%w at http://%s", errBridgeDown, listen)
	}
	if err != nil {
		return fmt.Errorf("asking the bridge at http://%s: %w", listen, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("the bridge at http://%s %w (it has no POST %s)", listen, errOlderBridge, path)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error string }
		_ = json.Unmarshal(body, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("the bridge at http://%s refused: %s", listen, e.Error)
	}
	return json.Unmarshal(body, out)
}

// askLoginCode asks the bridge at listen for a login code for the
// dashboard, with the token.
func askLoginCode(listen, token string) (string, error) {
	var resp struct{ Code string }
	if err := callBridge(listen, token, "/api/login/code", &resp); err != nil {
		if errors.Is(err, errBridgeDown) {
			return "", fmt.Errorf("%w — start it first", err)
		}
		return "", err
	}
	if resp.Code == "" {
		return "", fmt.Errorf("the bridge at http://%s sent no login code", listen)
	}
	return resp.Code, nil
}
