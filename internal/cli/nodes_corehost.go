package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/controlcli"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

// nodesCoreSecretsTransfer runs `fridolin nodes core-secrets-transfer SRC
// DST`: it always checks both sides first and prints what it found;
// with yes it then moves the secrets. Only names are ever printed: the
// core never sees a value.
func nodesCoreSecretsTransfer(cfg *config.Settings, src, dst string, yes, replace, asJSON bool, stdout, stderr io.Writer) int {
	ask := func(run bool) (httpapi.CoreSecretsTransferResponse, error) {
		var resp httpapi.CoreSecretsTransferResponse
		err := controlcli.API(cfg, http.MethodPost, "/api/nodes/core-secrets-transfer", httpapi.CoreSecretsTransferRequest{Source: src, Target: dst, Run: run, Replace: replace}, &resp)
		return resp, err
	}
	dry, err := ask(false)
	if err != nil {
		fmt.Fprintf(stderr, "fridolin nodes core-secrets-transfer: %v\n", err)
		return 1
	}
	if !yes || len(dry.Problems) > 0 {
		if asJSON {
			printCoreTransferJSON(stdout, dry)
		} else {
			writeCoreTransfer(stdout, dry)
		}
		if len(dry.Problems) > 0 {
			return 1
		}
		if !asJSON {
			fmt.Fprintf(stdout, "\nA dry run: nothing was sent. To move them: fridolin nodes core-secrets-transfer %s %s --yes\n", src, dst)
		}
		return 0
	}
	done, err := ask(true)
	if err != nil {
		fmt.Fprintf(stderr, "fridolin nodes core-secrets-transfer: %v\n", err)
		return 1
	}
	if asJSON {
		printCoreTransferJSON(stdout, done)
		return 0
	}
	writeCoreTransfer(stdout, done)
	return 0
}

func printCoreTransferJSON(w io.Writer, v httpapi.CoreSecretsTransferResponse) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// writeCoreTransfer prints a transfer's report: names and fingerprints,
// never a value.
func writeCoreTransfer(w io.Writer, r httpapi.CoreSecretsTransferResponse) {
	list := func(s []string) string {
		if len(s) == 0 {
			return "none"
		}
		return strings.Join(s, ", ")
	}
	fmt.Fprintf(w, "From %s (%s), device key %s\n  holds: %s\n  lacks: %s\n", r.Source.Name, r.Source.ID, r.Source.Fingerprint, list(r.Source.Stored), list(r.Source.Missing))
	fmt.Fprintf(w, "To   %s (%s), device key %s\n  holds: %s\n  lacks: %s\n", r.Target.Name, r.Target.ID, r.Target.Fingerprint, list(r.Target.Stored), list(r.Target.Missing))
	if r.Pinned {
		fmt.Fprintf(w, "%s trusts that key of %s.\n", r.Source.Name, r.Target.Name)
	}
	if len(r.Problems) > 0 {
		fmt.Fprintln(w, "\nIt cannot run:")
		for _, p := range r.Problems {
			fmt.Fprintf(w, "  - %s\n", p)
		}
	}
	if r.Ran {
		fmt.Fprintf(w, "\nMoved (move %s), end to end from %s to %s:\n  sent: %s\n  stored: %s\n  unchanged: %s\n", r.Move, r.Source.Name, r.Target.Name, list(r.Sent), list(r.Stored), list(r.Unchanged))
		fmt.Fprintf(w, "  secret files sent: %s\n  written: %s\n  kept (there already): %s\n", list(r.FilesSent), list(r.FilesStored), list(r.FilesKept))
		if r.FilesError != "" {
			fmt.Fprintf(w, "  the secret files could not be read: %s\n", r.FilesError)
		}
	}
}
