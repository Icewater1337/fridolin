package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// box.sh --update runs here against stubs of docker and gh, which record
// what they were asked in calls: an image must be checked on main before
// the box's .env names it.

const boxEnvBefore = "FRIDOLIN_BOX_NODE_IMAGE=old\nCOMPOSE_PROFILES=fride\n"

func runBoxScript(t *testing.T, signedIn, onMain bool, args ...string) (dir, calls, out string, ok bool) {
	t.Helper()
	home, stubs := t.TempDir(), t.TempDir()
	dir, calls = t.TempDir(), filepath.Join(t.TempDir(), "calls")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(boxEnvBefore), 0o600); err != nil {
		t.Fatal(err)
	}
	docker := `#!/bin/sh
printf 'docker %s\n' "$*" >>"$CALLS"
if [ "$1" = image ]; then for a; do ref=$a; done; echo "${ref%:*}@sha256:abc"; fi
`
	gh := `#!/bin/sh
printf 'gh %s\n' "$*" >>"$CALLS"
case "$1" in
auth) [ -n "$SIGNED_IN" ] ;;
api) echo Owner/fridolin ;;
attestation)
	# As gh does: an image built elsewhere fails only when the ref is pinned.
	case "$*" in *"--source-ref refs/heads/main"*) [ -n "$ON_MAIN" ] ;; esac ;;
esac
`
	for name, script := range map[string]string{"docker": docker, "gh": gh} {
		if err := os.WriteFile(filepath.Join(stubs, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := []string{filepath.Join(repoDir, "container", "box.sh"), "--registry", "ghcr.io/owner", "--version", "v1", "--update", "--yes", "--no-updater", "--dir", dir}
	cmd := exec.Command("/bin/sh", append(base, args...)...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + stubs + ":/usr/bin:/bin", "LANG=C", "CALLS=" + calls}
	if signedIn {
		cmd.Env = append(cmd.Env, "SIGNED_IN=1")
	}
	if onMain {
		cmd.Env = append(cmd.Env, "ON_MAIN=1")
	}
	b, err := cmd.CombinedOutput()
	got, _ := os.ReadFile(calls)
	return dir, string(got), string(b), err == nil
}

func boxEnv(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBoxScriptChecksProvenance(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("box.sh is for macOS and Linux; box.ps1 is Windows's")
	}
	t.Run("an image built on main starts", func(t *testing.T) {
		dir, calls, out, ok := runBoxScript(t, true, true)
		if !ok {
			t.Fatalf("box.sh failed:\n%s", out)
		}
		for _, img := range []string{"fridolin-core", "fridolin-fride"} {
			want := "gh attestation verify oci://ghcr.io/owner/" + img + "@sha256:abc --repo Owner/fridolin --signer-workflow Owner/fridolin/.github/workflows/image.yml --source-ref refs/heads/main\n"
			if !strings.Contains(calls, want) {
				t.Errorf("no %q in\n%s", want, calls)
			}
		}
		if env := boxEnv(t, dir); !strings.Contains(env, "FRIDOLIN_BOX_NODE_IMAGE=ghcr.io/owner/fridolin-core@sha256:abc\n") {
			t.Errorf(".env: %s", env)
		}
	})
	t.Run("an image built on another branch is refused", func(t *testing.T) {
		dir, calls, out, ok := runBoxScript(t, true, false)
		if ok {
			t.Fatalf("box.sh went on:\n%s", out)
		}
		if !strings.Contains(out, "no valid build provenance") {
			t.Errorf("it does not say why:\n%s", out)
		}
		if env := boxEnv(t, dir); env != boxEnvBefore {
			t.Errorf(".env changed: %s", env)
		}
		if strings.Contains(calls, "docker compose up") {
			t.Errorf("it started the box:\n%s", calls)
		}
	})
	t.Run("without a signed-in gh it refuses before pulling", func(t *testing.T) {
		dir, calls, out, ok := runBoxScript(t, false, true)
		if ok {
			t.Fatalf("box.sh went on:\n%s", out)
		}
		if !strings.Contains(out, "--insecure-skip-provenance") {
			t.Errorf("it does not name the way out:\n%s", out)
		}
		if strings.Contains(calls, "docker pull") {
			t.Errorf("it pulled:\n%s", calls)
		}
		if env := boxEnv(t, dir); env != boxEnvBefore {
			t.Errorf(".env changed: %s", env)
		}
	})
	t.Run("the explicit flag goes on unchecked, and says so", func(t *testing.T) {
		dir, calls, out, ok := runBoxScript(t, false, false, "--insecure-skip-provenance")
		if !ok {
			t.Fatalf("box.sh failed:\n%s", out)
		}
		if !strings.Contains(out, "WARNING") {
			t.Errorf("no warning:\n%s", out)
		}
		if strings.Contains(calls, "gh ") {
			t.Errorf("it asked gh:\n%s", calls)
		}
		if env := boxEnv(t, dir); !strings.Contains(env, "@sha256:abc") {
			t.Errorf(".env: %s", env)
		}
	})
}

// No PowerShell runs box.ps1 in the tests: hold it to the same check by its
// text.
func TestBoxPowerShellScriptChecksProvenance(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoDir, "container", "box.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--source-ref refs/heads/main", "[switch]$InsecureSkipProvenance", "if (-not $gh -or $LASTEXITCODE -ne 0) { Die"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("box.ps1 has no %q", want)
		}
	}
}
