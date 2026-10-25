package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/controlcli"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

// moveAPI asks the core; nodesMove's tests hand in their own.
type moveAPI func(method, path string, body, out any) error

// moveFollow is how long `nodes move` follows a move at most, and how
// often it asks.
var (
	moveFollow      = 2 * time.Hour
	moveFollowEvery = 5 * time.Second
)

// nodesMoveCheck runs `fridolin nodes move-check SRC DST`: the pre-flight
// report, read only. It exits 1 when something blocks the move.
func nodesMoveCheck(api moveAPI, src, dst string, asJSON bool, stdout, stderr io.Writer) int {
	var rep nodes.MoveReport
	if err := api(http.MethodPost, "/api/nodes/move-check", httpapi.MoveRequest{Source: src, Target: dst}, &rep); err != nil {
		fmt.Fprintf(stderr, "fridolin nodes move-check: %v\n", err)
		return 1
	}
	if asJSON {
		printIndented(stdout, rep)
	} else {
		writeMoveReport(stdout, rep)
	}
	if len(rep.Blocking) > 0 {
		return 1
	}
	return 0
}

// nodesMove runs `fridolin nodes move SRC DST`: the pre-flight, and with
// yes the move itself, which it follows to its end unless noWait. The
// core stops on SRC on the way and a core on DST answers at the same
// address (the tailnet name moves with the export), so a CLI that asks
// over the tailnet sees the move end; one that asks 127.0.0.1 on SRC
// waits for the core that comes back there after a rollback, or until
// it gives up and says where to look.
func nodesMove(api moveAPI, src, dst string, yes, noWait, asJSON bool, stdout, stderr io.Writer) int {
	if code := nodesMoveCheck(api, src, dst, asJSON, stdout, stderr); code != 0 || !yes {
		if code == 0 && !asJSON {
			fmt.Fprintf(stdout, "\nA dry run: nothing changed. To move the core: fridolin nodes move %s %s --yes\n", src, dst)
		}
		return code
	}
	var resp httpapi.MoveResponse
	if err := api(http.MethodPost, "/api/nodes/move", httpapi.MoveRequest{Source: src, Target: dst}, &resp); err != nil {
		fmt.Fprintf(stderr, "fridolin nodes move: %v\n", err)
		return 1
	}
	id := resp.ID
	fmt.Fprintf(stdout, "\nMove %s runs. Follow it with: fridolin nodes move-status %s\n", id, id)
	if noWait {
		return 0
	}
	return followMove(api, id, asJSON, stdout, stderr)
}

// followMove prints a move's steps as they come until it ends.
func followMove(api moveAPI, id string, asJSON bool, stdout, stderr io.Writer) int {
	shown, away := 0, false
	end := time.Now().Add(moveFollow)
	for time.Now().Before(end) {
		var rec nodes.MoveRecord
		err := api(http.MethodGet, "/api/nodes/moves/"+id, nil, &rec)
		var apiErr *controlcli.APIError
		switch {
		case err == nil:
			if away {
				fmt.Fprintln(stdout, "The core answers again.")
				away = false
			}
			for ; shown < len(rec.Steps); shown++ {
				s := rec.Steps[shown]
				fmt.Fprintf(stdout, "%s  %s: %s\n", s.At.Local().Format("15:04:05"), s.Step, s.Detail)
			}
			if nodes.MoveFinal(rec.State) {
				if asJSON {
					printIndented(stdout, rec)
				} else {
					fmt.Fprintf(stdout, "\nMove %s: %s. %s\n", id, rec.State, rec.Detail)
				}
				if rec.State == nodes.MoveDone {
					return 0
				}
				return 1
			}
		case errors.As(err, &apiErr) && apiErr.Status != http.StatusServiceUnavailable:
			fmt.Fprintf(stderr, "fridolin nodes move: %v\n", err)
			return 1
		default:
			// The core stops on the source on the way.
			if !away {
				fmt.Fprintf(stdout, "The core does not answer (it stops on the source as planned); waiting for it at the same address... (%v)\n", err)
				away = true
			}
		}
		time.Sleep(moveFollowEvery)
	}
	fmt.Fprintf(stderr, "fridolin nodes move: move %s did not end within %s; ask the core that runs now: fridolin nodes move-status %s\n", id, moveFollow, id)
	return 1
}

// nodesMoveStatus runs `fridolin nodes move-status ID`.
func nodesMoveStatus(api moveAPI, id string, asJSON bool, stdout, stderr io.Writer) int {
	var rec nodes.MoveRecord
	if err := api(http.MethodGet, "/api/nodes/moves/"+id, nil, &rec); err != nil {
		fmt.Fprintf(stderr, "fridolin nodes move-status: %v\n", err)
		return 1
	}
	if asJSON {
		printIndented(stdout, rec)
		return 0
	}
	fmt.Fprintf(stdout, "Move %s from %s (%s) to %s (%s): %s\n", rec.ID, rec.SourceName, rec.Source, rec.TargetName, rec.Target, rec.State)
	if rec.Detail != "" {
		fmt.Fprintf(stdout, "  %s\n", rec.Detail)
	}
	for _, s := range rec.Steps {
		fmt.Fprintf(stdout, "%s  %s: %s\n", s.At.Local().Format("2006-01-02 15:04:05"), s.Step, s.Detail)
	}
	return 0
}

func printIndented(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// writeMoveReport prints the pre-flight: names, versions, fingerprints,
// the secrets by name only.
func writeMoveReport(w io.Writer, r nodes.MoveReport) {
	end := func(label string, e nodes.MoveEnd) {
		fmt.Fprintf(w, "%s %s (%s), %s/%s, version %s, device key %s", label, e.Name, e.ID, e.OS, e.Arch, e.Version, e.Fingerprint)
		if e.Box {
			fmt.Fprint(w, ", a box")
		}
		if !e.Online {
			fmt.Fprint(w, ", offline")
		}
		fmt.Fprintln(w)
		switch {
		case e.Core != nil:
			lease := "no lease"
			if l := e.Core.Lease; l != nil {
				lease = "lease " + l.State + " by " + l.Device
				if e.Core.HeldBy() {
					lease = "holds the lease"
				}
			}
			fmt.Fprintf(w, "  core: data dir %s, %s\n", e.Core.Device, lease)
		case e.CoreError != "":
			fmt.Fprintf(w, "  core: %s\n", e.CoreError)
		}
		if e.DiskFree > 0 {
			fmt.Fprintf(w, "  free: %.1f GB\n", float64(e.DiskFree)/(1<<30))
		}
	}
	end("From", r.Source)
	end("To  ", r.Target)
	img := r.Image
	if img == "" {
		img = "a local build of " + r.Version
	}
	fmt.Fprintf(w, "Image: %s (%s)\n", img, map[bool]string{true: "on the target", false: "pulled before anything stops"}[r.ImagePresent])
	list := func(s []string) string {
		if len(s) == 0 {
			return "none"
		}
		return strings.Join(s, ", ")
	}
	trust := "the owner confirmed it as a core host"
	switch {
	case r.Secrets.Pinned:
		trust = r.Source.Name + " pins its key"
	case r.Secrets.NeedsConfirm:
		trust = "it never held the core: only the owner's !movecore vote in WhatsApp lets it hold the secrets, not this command"
	case !r.Secrets.Confirmed:
		trust = "not confirmed"
	}
	fmt.Fprintf(w, "Host secrets: the target lacks %s; %s\n", list(r.Secrets.Target.Missing), trust)
	if r.SecretFilesDir != "" {
		fmt.Fprintf(w, "Secret files (%s): %s\n", r.SecretFilesDir, list(r.SecretFiles))
	}
	if r.Outage != "" {
		fmt.Fprintf(w, "Offline: %s\n", r.Outage)
	}
	section := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(w, "\n%s\n", title)
		for _, it := range items {
			fmt.Fprintf(w, "  - %s\n", it)
		}
	}
	section("Does not work on the target:", r.MachineBound)
	section("Note:", r.Warnings)
	section("It cannot run:", r.Blocking)
}
