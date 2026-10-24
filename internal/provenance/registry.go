package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Registry looks images up on GHCR without a login, as a public package
// allows.
type Registry struct {
	// Base is the registry's address, https://ghcr.io when empty.
	Base string
	// HTTP fetches; a client with a timeout when nil.
	HTTP *http.Client
}

var (
	repoPathRE = regexp.MustCompile(`^ghcr\.io/([a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._/-]*)$`)
	tagRE      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	sha256RE   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// manifestTypes are the manifests a tag may name, multi-arch first.
var manifestTypes = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// Digest is the image repo (ghcr.io/owner/name) of tag, pinned by digest:
// ghcr.io/owner/name@sha256:….
func (r Registry) Digest(ctx context.Context, repo, tag string) (string, error) {
	m := repoPathRE.FindStringSubmatch(repo)
	if m == nil {
		return "", fmt.Errorf("registry: %q is not a ghcr.io image", repo)
	}
	if !tagRE.MatchString(tag) {
		return "", fmt.Errorf("registry: %q is not a tag", tag)
	}
	base := strings.TrimRight(r.Base, "/")
	if base == "" {
		base = "https://ghcr.io"
	}
	client := r.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	// An anonymous token for pulling, as GHCR hands one to everybody for
	// a public package.
	tokURL := base + "/token?service=ghcr.io&scope=" + url.QueryEscape("repository:"+m[1]+":pull")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry: %w", err)
	}
	var tok struct {
		Token string `json:"token"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || err != nil || tok.Token == "" {
		return "", fmt.Errorf("registry: no pull token for %s (%s): is the package public?", repo, resp.Status)
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodHead, base+"/v2/"+m[1]+"/manifests/"+tag, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	req.Header.Set("Accept", manifestTypes)
	resp, err = client.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry: %w", err)
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", fmt.Errorf("registry: %s has no %s (CI publishes the versions of main)", repo, tag)
	default:
		return "", fmt.Errorf("registry: %s:%s: %s", repo, tag, resp.Status)
	}
	d := resp.Header.Get("Docker-Content-Digest")
	if !sha256RE.MatchString(d) {
		return "", errors.New("registry: the answer names no digest")
	}
	return repo + "@" + d, nil
}
