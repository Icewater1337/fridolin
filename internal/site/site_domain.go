package site

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// DomainCheck is what checking the domain from this machine found: the
// last step of linking one (docs/SITE.md, "From zero to your domain").
type DomainCheck struct {
	URL    string `json:"url"`
	OK     bool   `json:"ok"`
	Status int    `json:"status,omitempty"`
	// Error says what is wrong and what to do about it.
	Error string `json:"error,omitempty"`
}

// domainCheckTimeout bounds one check.
const domainCheckTimeout = 15 * time.Second

// CheckDomain asks https://<domain>/_fridolin/health, through the
// internet as anyone would, whether the site answers there.
func (m *SiteManager) CheckDomain(ctx context.Context) DomainCheck {
	s := m.Settings()
	if s.Domain == "" {
		return DomainCheck{Error: "Enter the domain first (step 1)."}
	}
	url := "https://" + s.Domain + "/_fridolin/health"
	if m.domainURL != nil {
		url = m.domainURL(s.Domain)
	}
	out := DomainCheck{URL: url}
	ctx, cancel := context.WithTimeout(ctx, domainCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	client := m.domainClient
	if client == nil {
		client = &http.Client{Timeout: domainCheckTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		var dns *net.DNSError
		switch {
		case errors.As(err, &dns):
			out.Error = fmt.Sprintf("%s does not resolve yet. Give the tunnel a public host name for it in Cloudflare (step 2), which adds the DNS record; a new one can take a few minutes.", s.Domain)
		default:
			out.Error = fmt.Sprintf("%s does not answer: %v", s.Domain, err)
		}
		return out
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	out.Status = resp.StatusCode
	switch {
	case resp.StatusCode == http.StatusOK && strings.TrimSpace(string(body)) == "ok":
		out.OK = true
	case resp.StatusCode == http.StatusBadGateway || resp.StatusCode == 530 || resp.StatusCode == http.StatusServiceUnavailable:
		out.Error = fmt.Sprintf("Cloudflare answers for %s but cannot reach the site (%d). Is the site on, the tunnel running (step 2), and its public host name's service http://%s?", s.Domain, resp.StatusCode, s.Listen)
	default:
		out.Error = fmt.Sprintf("%s answers %d, but not as this site: does its public host name point at the tunnel this Fridolin runs, with the service http://%s?", s.Domain, resp.StatusCode, s.Listen)
	}
	return out
}
