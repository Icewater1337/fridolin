package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

// SchedulesAPI serves the brains' scheduled agents (docs/BRAINS.md,
// "Scheduled agents"): the list with their reminders, run now, pause and
// resume, each agent's runs and each run's log, and editing an agent,
// which writes brain.json as every edit in the dashboard does.
type SchedulesAPI struct {
	List   func(context.Context) (any, error)
	Run    func(ctx context.Context, brain, name, by string) (any, error)
	Pause  func(ctx context.Context, brain, name string, paused bool) (any, error)
	Runs   func(ctx context.Context, brain, name string, limit int) (any, error)
	Log    func(ctx context.Context, brain, name, run string) (any, error)
	Put    func(ctx context.Context, brain, name string, s manifest.Schedule) (any, error)
	Remove func(ctx context.Context, brain, name string) (any, error)
}

// ScheduleRunRequest is the body of POST /api/schedules/{brain}/{name}/run.
type ScheduleRunRequest struct {
	// Trigger is "cli" for fridolin schedules run; anything else is the
	// dashboard.
	Trigger string `json:"trigger,omitempty"`
}

// Register adds the routes.
func (a *SchedulesAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/schedules", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.List(r.Context())
		if err != nil {
			writeKindErr(w, err)
			return
		}
		WriteJSONIfChanged(w, r, out)
	})
	mux.HandleFunc("POST /api/schedules/{brain}/{name}/run", func(w http.ResponseWriter, r *http.Request) {
		var req ScheduleRunRequest
		if err := DecodeOptionalJSON(r, &req); err != nil {
			WriteErr(w, http.StatusBadRequest, err.Error())
			return
		}
		by := "dashboard"
		if req.Trigger == "cli" {
			by = "cli"
		}
		out, err := a.Run(r.Context(), r.PathValue("brain"), r.PathValue("name"), by)
		writeResult(w, out, err)
	})
	pause := func(paused bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			out, err := a.Pause(r.Context(), r.PathValue("brain"), r.PathValue("name"), paused)
			writeResult(w, out, err)
		}
	}
	mux.HandleFunc("POST /api/schedules/{brain}/{name}/pause", pause(true))
	mux.HandleFunc("POST /api/schedules/{brain}/{name}/resume", pause(false))
	mux.HandleFunc("GET /api/schedules/{brain}/{name}/runs", func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if s := r.URL.Query().Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > 1000 {
				WriteErr(w, http.StatusBadRequest, "limit must be a number from 1 to 1000")
				return
			}
			limit = n
		}
		out, err := a.Runs(r.Context(), r.PathValue("brain"), r.PathValue("name"), limit)
		if err != nil {
			writeKindErr(w, err)
			return
		}
		WriteJSONIfChanged(w, r, out)
	})
	mux.HandleFunc("GET /api/schedules/{brain}/{name}/runs/{run}/log", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Log(r.Context(), r.PathValue("brain"), r.PathValue("name"), r.PathValue("run"))
		if err != nil {
			writeKindErr(w, err)
			return
		}
		WriteJSONIfChanged(w, r, out)
	})
	mux.HandleFunc("PUT /api/schedules/{brain}/{name}", func(w http.ResponseWriter, r *http.Request) {
		var s manifest.Schedule
		if err := manifest.JSONError(DecodeJSON(r, &s)); err != nil {
			WriteErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := a.Put(r.Context(), r.PathValue("brain"), r.PathValue("name"), s)
		writeResult(w, out, err)
	})
	mux.HandleFunc("DELETE /api/schedules/{brain}/{name}", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Remove(r.Context(), r.PathValue("brain"), r.PathValue("name"))
		writeResult(w, out, err)
	})
}
