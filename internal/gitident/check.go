package gitident

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CheckTimeout bounds a check when the caller's context has no deadline.
const CheckTimeout = 15 * time.Second

// Check asks GitLab who the token is (GET <base_url>/api/v4/user, with
// PRIVATE-TOKEN) and returns the username. It follows no redirect: a 3xx
// is an answer like any other, so the token never goes on to another
// host. A nil client is a plain one.
func Check(ctx context.Context, client *http.Client, c Connection) (string, error) {
	if c.Token == "" || c.BaseURL == "" {
		return "", errors.New("no GitLab connection: set one on the dashboard's Integrations page")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, CheckTimeout)
		defer cancel()
	}
	hc := &http.Client{}
	if client != nil {
		cp := *client
		hc = &cp
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v4/user", nil)
	if err != nil {
		return "", fmt.Errorf("GitLab's address: %v", err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("GitLab at %s did not answer in time", c.Host())
		}
		return "", fmt.Errorf("GitLab at %s does not answer: %s", c.Host(), scrub(err.Error(), c.Token))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return "", errors.New("GitLab says the token is wrong or expired (401)")
	case resp.StatusCode == http.StatusForbidden:
		return "", errors.New("GitLab refused the token (403): it needs the read_user or api scope")
	case resp.StatusCode/100 == 3:
		return "", fmt.Errorf("GitLab at %s answered with a redirect (%d): check the address", c.Host(), resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("GitLab at %s answered %d", c.Host(), resp.StatusCode)
	}
	var u struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(body, &u); err != nil || u.Username == "" {
		return "", fmt.Errorf("GitLab at %s answered, but not as GitLab's API does: check the address", c.Host())
	}
	return u.Username, nil
}

// scrub takes the token out of an error text, should anything have
// echoed it.
func scrub(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "[token]")
}
