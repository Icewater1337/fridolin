package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/controlcli"
)

// vaultMigrateWait bounds how long migrate-encryption waits for its job:
// a first copy of a large cold tier takes hours, and the job goes on
// after the command gives up waiting.
var vaultMigrateWait = 12 * time.Hour

// vaultMigratePoll is how often it looks.
var vaultMigratePoll = 2 * time.Second

// vaultMigrate runs fridolin vault migrate-encryption: it starts the job
// on the running bridge and prints its report when it ends.
func vaultMigrate(cfg *config.Settings, args []string, dryRun, run, deleteOld bool, stdout, stderr io.Writer) int {
	switch {
	case len(args) > 0:
		fmt.Fprintf(stderr, "fridolin vault migrate-encryption takes no arguments, got %q\n", args)
		return 2
	case dryRun == run:
		fmt.Fprintln(stderr, "fridolin vault migrate-encryption: pass --dry-run or --run")
		return 2
	case dryRun && deleteOld:
		fmt.Fprintln(stderr, "fridolin vault migrate-encryption: a dry run deletes nothing; --delete-old goes with --run")
		return 2
	}
	start := time.Now()
	var resp struct{ Status string }
	body := map[string]bool{"dry_run": dryRun, "delete_old": deleteOld}
	if err := controlcli.VaultCall(cfg, http.MethodPost, "/api/vault/migrate-encryption", body, &resp, 30*time.Second); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stderr, "migrate-encryption started; waiting for it (Ctrl-C stops waiting, not the job)…")
	for deadline := start.Add(vaultMigrateWait); time.Now().Before(deadline); time.Sleep(vaultMigratePoll) {
		var raw json.RawMessage
		if err := controlcli.VaultCall(cfg, http.MethodGet, "/api/vault/status", nil, &raw, 30*time.Second); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		text, done, err := app.EncryptionMigrationText(raw, start)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if done {
			fmt.Fprint(stdout, text)
			var st struct {
				EncryptionMigration struct {
					OK bool `json:"ok"`
				} `json:"encryption_migration"`
			}
			if json.Unmarshal(raw, &st) == nil && !st.EncryptionMigration.OK {
				return 1
			}
			return 0
		}
	}
	fmt.Fprintln(stderr, "still running; fridolin vault status shows how it went")
	return 0
}
