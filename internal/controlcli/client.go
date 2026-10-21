package controlcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"syscall"
	"time"
)

// ErrOlderBridge is a bridge that answers 404 for an endpoint this binary
// asks for: one that runs an older build, such as the bridge setup.sh
// leaves running when its install is declined.
var ErrOlderBridge = errors.New("runs a build older than this fridolin")

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
		return fmt.Errorf("%w at http://%s", ErrBridgeDown, listen)
	}
	if err != nil {
		return fmt.Errorf("asking the bridge at http://%s: %w", listen, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("the bridge at http://%s %w (it has no POST %s)", listen, ErrOlderBridge, path)
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

// AskLoginCode asks the bridge at listen for a login code for the
// dashboard, with the token.
func AskLoginCode(listen, token string) (string, error) {
	var resp struct{ Code string }
	if err := callBridge(listen, token, "/api/login/code", &resp); err != nil {
		if errors.Is(err, ErrBridgeDown) {
			return "", fmt.Errorf("%w — start it first", err)
		}
		return "", err
	}
	if resp.Code == "" {
		return "", fmt.Errorf("the bridge at http://%s sent no login code", listen)
	}
	return resp.Code, nil
}

// AskTailnetLoginLink asks the bridge at listen for a sign-in link at the
// dashboard's tailnet address, with the token: a code that works once,
// within httpapi.RemoteLoginCodeLifetime, from another device of the
// tailnet.
func AskTailnetLoginLink(listen, token string) (string, error) {
	var resp struct{ Code, URL string }
	if err := callBridge(listen, token, "/api/login/code?tailnet=1", &resp); err != nil {
		if errors.Is(err, ErrBridgeDown) {
			return "", fmt.Errorf("%w — start it first", err)
		}
		return "", err
	}
	if resp.URL == "" {
		// A bridge older than this binary ignores ?tailnet=1.
		return "", fmt.Errorf("the bridge at http://%s %w (it sent no tailnet link)", listen, ErrOlderBridge)
	}
	return resp.URL, nil
}

var ErrBridgeDown = errors.New("the bridge does not answer")
