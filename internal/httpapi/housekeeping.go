package httpapi

import (
	"context"
	"net/http"
)

// HousekeepingRunRequest is the body of POST /api/housekeeping/run.
type HousekeepingRunRequest struct {
	// DryRun true only reports, whatever housekeeping.dry_run says;
	// absent or false runs as the setting says.
	DryRun bool `json:"dry_run"`
	// Trigger is "cli" for fridolin housekeeping run; anything else is the
	// dashboard.
	Trigger string `json:"trigger,omitempty"`
}

// HousekeepingAPI serves the housekeeping job: its last report and a run
// now, which answers with the run's report once it is over.
type HousekeepingAPI struct {
	Status func(context.Context) (any, error)
	Run    func(ctx context.Context, dryRun bool, trigger string) (any, error)
}

// Register adds the job's routes.
func (a *HousekeepingAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/housekeeping", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Status(r.Context())
		if err != nil {
			writeResult(w, nil, err)
			return
		}
		WriteJSONIfChanged(w, r, out)
	})
	mux.HandleFunc("POST /api/housekeeping/run", func(w http.ResponseWriter, r *http.Request) {
		var req HousekeepingRunRequest
		if err := DecodeOptionalJSON(r, &req); err != nil {
			WriteErr(w, http.StatusBadRequest, err.Error())
			return
		}
		trigger := "dashboard"
		if req.Trigger == "cli" {
			trigger = "cli"
		}
		out, err := a.Run(r.Context(), req.DryRun, trigger)
		writeResult(w, out, err)
	})
}
