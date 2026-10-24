// Package provenance checks where an image came from: that GitHub's
// attestations say a repository's workflow built its digest on main
// (docs/CORE.md, "On GHCR"). It fetches the attestation bundles from
// GitHub's public API, which needs no login for a public repository, and
// has GitHub's CLI verify them (`gh attestation verify --bundle`, which
// needs no login either), so fridolin carries no Sigstore library.
package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/klauspost/compress/snappy"
)

// Workflow is the workflow that builds and attests fridolin's images.
const Workflow = ".github/workflows/image.yml"

// Verifier checks images. Its zero value asks api.github.com and runs gh
// from the PATH.
type Verifier struct {
	// API is GitHub's API, https://api.github.com when empty.
	API string
	// HTTP fetches; http.DefaultClient with a timeout when nil.
	HTTP *http.Client
	// Run runs gh with args and returns its combined output; nil runs
	// the gh on the PATH.
	Run func(ctx context.Context, args ...string) ([]byte, error)
	// TempDir holds the bundles while gh reads them; os.TempDir when "".
	TempDir string
}

var (
	digestRE = regexp.MustCompile(`@(sha256:[0-9a-f]{64})$`)
	repoRE   = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
)

// maxBundle bounds one attestation bundle.
const maxBundle = 4 << 20

// Verify checks that image, named by its digest, was built from repo
// (owner/name) by Workflow on its main branch.
func (v Verifier) Verify(ctx context.Context, image, repo string) error {
	m := digestRE.FindStringSubmatch(image)
	if m == nil {
		return fmt.Errorf("provenance: %s is not named by its digest", image)
	}
	if !repoRE.MatchString(repo) {
		return fmt.Errorf("provenance: %q is not a repository (owner/name)", repo)
	}
	bundles, err := v.bundles(ctx, repo, m[1])
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp(v.TempDir, "provenance-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "bundles.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(bundles, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	out, err := v.run(ctx, "attestation", "verify", "oci://"+image, "--bundle", path,
		"--repo", repo, "--signer-workflow", repo+"/"+Workflow, "--source-ref", "refs/heads/main")
	if err != nil {
		return fmt.Errorf("provenance: %s was not built by %s's %s on main: %v %s", image, repo, Workflow, err, strings.TrimSpace(lastLines(string(out), 3)))
	}
	return nil
}

func (v Verifier) run(ctx context.Context, args ...string) ([]byte, error) {
	if v.Run != nil {
		return v.Run(ctx, args...)
	}
	gh, err := exec.LookPath("gh")
	if err != nil {
		return nil, errors.New("GitHub's CLI (gh) is not installed; it checks the provenance, with no login needed")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, gh, args...)
	// No prompt and no update check; a login is not needed with --bundle.
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1")
	return cmd.CombinedOutput()
}

func (v Verifier) client() *http.Client {
	if v.HTTP != nil {
		return v.HTTP
	}
	return &http.Client{Timeout: time.Minute}
}

// bundles are the Sigstore bundles GitHub keeps for digest in repo, one
// JSON document each.
func (v Verifier) bundles(ctx context.Context, repo, digest string) ([]string, error) {
	api := strings.TrimRight(v.API, "/")
	if api == "" {
		api = "https://api.github.com"
	}
	body, err := v.get(ctx, api+"/repos/"+repo+"/attestations/"+digest, "application/vnd.github+json")
	if errors.Is(err, errNotFound) {
		return nil, fmt.Errorf("provenance: %s has no attestations for %s", repo, digest)
	}
	if err != nil {
		return nil, err
	}
	var list struct {
		Attestations []struct {
			Bundle    json.RawMessage `json:"bundle"`
			BundleURL string          `json:"bundle_url"`
		} `json:"attestations"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("provenance: GitHub's answer: %w", err)
	}
	var out []string
	for _, a := range list.Attestations {
		if b := strings.TrimSpace(string(a.Bundle)); b != "" && b != "null" {
			out = append(out, compact(a.Bundle))
			continue
		}
		if !strings.HasPrefix(a.BundleURL, "https://") {
			continue
		}
		raw, err := v.get(ctx, a.BundleURL, "")
		if err != nil {
			return nil, err
		}
		// GitHub stores them snappy-compressed (.json.sn).
		if dec, err := snappy.Decode(nil, raw); err == nil {
			raw = dec
		}
		if !json.Valid(raw) {
			return nil, errors.New("provenance: an attestation bundle does not read")
		}
		out = append(out, compact(raw))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("provenance: %s has no attestations for %s", repo, digest)
	}
	return out, nil
}

var errNotFound = errors.New("not found")

func (v Verifier) get(ctx context.Context, url, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := v.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("provenance: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provenance: GET %s: %s", strings.SplitN(url, "?", 2)[0], resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBundle+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBundle {
		return nil, errors.New("provenance: an answer too large for an attestation")
	}
	return data, nil
}

func compact(raw []byte) string {
	var b strings.Builder
	var v any
	if json.Unmarshal(raw, &v) == nil {
		if out, err := json.Marshal(v); err == nil {
			b.Write(out)
			return b.String()
		}
	}
	return strings.ReplaceAll(string(raw), "\n", "")
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// Repo is the repository a box source or registry names: owner/name of
// a raw.githubusercontent.com source (https://raw.githubusercontent.com/
// OWNER/NAME/COMMIT), else OWNER/fridolin of a ghcr.io/OWNER registry;
// "" when neither says.
func Repo(source, registry string) string {
	if rest, ok := strings.CutPrefix(source, "https://raw.githubusercontent.com/"); ok {
		parts := strings.Split(rest, "/")
		if len(parts) >= 2 && repoRE.MatchString(parts[0]+"/"+parts[1]) {
			return parts[0] + "/" + parts[1]
		}
	}
	if owner, ok := strings.CutPrefix(registry, "ghcr.io/"); ok && !strings.Contains(owner, "/") && owner != "" {
		return owner + "/fridolin"
	}
	return ""
}
