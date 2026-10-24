package provenance

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/klauspost/compress/snappy"
)

const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestVerifyHandsGitHubsBundlesToGh(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/fridolin/attestations/" + digest:
			_, _ = w.Write([]byte(`{"attestations":[{"bundle":{"a":1}},{"bundle":null,"bundle_url":"` + srv.URL + `/b.json.sn"}]}`))
		case "/b.json.sn":
			_, _ = w.Write(snappy.Encode(nil, []byte(`{"b": 2}`)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	var args []string
	var bundles string
	v := Verifier{API: srv.URL, HTTP: srv.Client(), TempDir: t.TempDir(), Run: func(_ context.Context, a ...string) ([]byte, error) {
		args = a
		i := slices.Index(a, "--bundle")
		data, _ := os.ReadFile(a[i+1])
		bundles = string(data)
		return nil, nil
	}}
	img := "ghcr.io/owner/fridolin-core@" + digest
	if err := v.Verify(context.Background(), img, "owner/fridolin"); err != nil {
		t.Fatal(err)
	}
	if bundles != "{\"a\":1}\n{\"b\":2}\n" {
		t.Fatalf("bundles %q", bundles)
	}
	got := strings.Join(args, " ")
	for _, want := range []string{"attestation verify oci://" + img, "--repo owner/fridolin", "--signer-workflow owner/fridolin/.github/workflows/image.yml", "--source-ref refs/heads/main"} {
		if !strings.Contains(got, want) {
			t.Errorf("gh %s lacks %q", got, want)
		}
	}

	v.Run = func(context.Context, ...string) ([]byte, error) { return []byte("x\nno match"), errors.New("exit 1") }
	if err := v.Verify(context.Background(), img, "owner/fridolin"); err == nil || !strings.Contains(err.Error(), "no match") {
		t.Fatalf("a failed check: %v", err)
	}
	if err := v.Verify(context.Background(), "ghcr.io/owner/fridolin-core@sha256:"+strings.Repeat("9", 64), "owner/fridolin"); err == nil || !strings.Contains(err.Error(), "no attestations") {
		t.Fatalf("an image without attestations: %v", err)
	}
	if err := v.Verify(context.Background(), "ghcr.io/owner/fridolin-core:latest", "owner/fridolin"); err == nil {
		t.Fatal("an image by tag passed")
	}
	if err := (Verifier{}).Verify(context.Background(), img, "owner/fridolin;x"); err == nil {
		t.Fatal("a bad repository passed")
	}
}

func TestRepo(t *testing.T) {
	for _, tc := range []struct{ source, registry, want string }{
		{"https://raw.githubusercontent.com/Owner/fridolin/abc", "ghcr.io/owner", "Owner/fridolin"},
		{"", "ghcr.io/owner", "owner/fridolin"},
		{"", "docker.io/owner", ""},
		{"", "ghcr.io/a/b", ""},
	} {
		if got := Repo(tc.source, tc.registry); got != tc.want {
			t.Errorf("Repo(%q, %q) = %q, want %q", tc.source, tc.registry, got, tc.want)
		}
	}
}

func TestRegistryDigest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") != "repository:owner/fridolin-core:pull" {
				t.Errorf("scope %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"token":"anon"}`))
		case r.URL.Path == "/v2/owner/fridolin-core/manifests/v1" && r.Header.Get("Authorization") == "Bearer anon" && strings.Contains(r.Header.Get("Accept"), "image.index"):
			w.Header().Set("Docker-Content-Digest", digest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	reg := Registry{Base: srv.URL, HTTP: srv.Client()}
	got, err := reg.Digest(context.Background(), "ghcr.io/owner/fridolin-core", "v1")
	if err != nil || got != "ghcr.io/owner/fridolin-core@"+digest {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := reg.Digest(context.Background(), "ghcr.io/owner/fridolin-core", "v2"); err == nil || !strings.Contains(err.Error(), "has no v2") {
		t.Fatalf("a missing tag: %v", err)
	}
	if _, err := reg.Digest(context.Background(), "docker.io/owner/x", "v1"); err == nil {
		t.Fatal("another registry passed")
	}
}
