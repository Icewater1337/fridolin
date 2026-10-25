package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/controlcli"
)

// secretFolderView is a folder of a version, as the bridge reports it.
type secretFolderView struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Label  string `json:"label"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
}

// secretRestore is POST /api/vault/secret-folders/restore's answer.
type secretRestore struct {
	Version  string             `json:"version"`
	To       string             `json:"to"`
	Force    bool               `json:"force"`
	Replaced string             `json:"replaced"`
	Folders  []secretFolderView `json:"folders"`
	Files    int                `json:"files"`
	Bytes    int64              `json:"bytes"`
	Kept     int                `json:"kept"`
}

// vaultRestoreSecrets is fridolin vault restore-secrets.
func vaultRestoreSecrets(cfg *config.Settings, args []string, to, at string, force, list bool, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "fridolin vault restore-secrets takes no arguments, got %q: the folder goes with --to\n", args)
		return 2
	}
	if list {
		if to != "" || at != "" || force {
			fmt.Fprintln(stderr, "fridolin vault restore-secrets --list restores nothing: leave out --to, --at and --force")
			return 2
		}
		return vaultListSecrets(cfg, stdout, stderr)
	}
	if force && to != "" {
		fmt.Fprintln(stderr, "fridolin vault restore-secrets: --force restores into the live folders; leave out --to")
		return 2
	}
	if to != "" {
		abs, err := filepath.Abs(to)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		to = abs
	}
	var res secretRestore
	body := map[string]any{"to": to, "at": at, "force": force}
	if err := controlcli.VaultCall(cfg, http.MethodPost, "/api/vault/secret-folders/restore", body, &res, 30*time.Minute); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "restored %s into %s: %d files, %d bytes\n", res.Version, res.To, res.Files, res.Bytes)
	for _, f := range res.Folders {
		where := filepath.Join(res.To, f.Label)
		if res.Force {
			where = f.Path
		}
		fmt.Fprintf(stdout, "  %s: %s, backed up from %s (%s)\n", f.Label, where, f.Path, f.Source)
	}
	if res.Force {
		fmt.Fprintf(stdout, "what the live folders held before is in %s", res.Replaced)
		if res.Kept > 0 {
			fmt.Fprintf(stdout, "; %d files the version does not have stayed as they were", res.Kept)
		}
		fmt.Fprintln(stdout)
	}
	return 0
}

// vaultListSecrets prints the versions the status lists.
func vaultListSecrets(cfg *config.Settings, stdout, stderr io.Writer) int {
	var raw json.RawMessage
	err := controlcli.VaultCall(cfg, http.MethodGet, "/api/vault/status", nil, &raw, 30*time.Second)
	if errors.Is(err, controlcli.ErrBridgeDown) {
		raw, err = app.ReadVaultStatusFile(cfg.DataDir, os.ReadFile)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var st struct {
		SecretFolders *struct {
			Versions []struct {
				Version string    `json:"version"`
				At      time.Time `json:"at"`
				Bytes   int64     `json:"bytes"`
			} `json:"versions"`
		} `json:"secret_folders"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		fmt.Fprintf(stderr, "reading the vault's status: %v\n", err)
		return 1
	}
	if st.SecretFolders == nil || len(st.SecretFolders.Versions) == 0 {
		fmt.Fprintln(stdout, "no version of the secret folders yet")
		return 0
	}
	for _, v := range st.SecretFolders.Versions {
		fmt.Fprintf(stdout, "%s  %s  %d bytes\n", v.At.Local().Format("2006-01-02 15:04:05"), v.Version, v.Bytes)
	}
	return 0
}
