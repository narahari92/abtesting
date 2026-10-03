package httpapi

import (
	"context"
	"net/http"
	"time"

	"variantsvc/internal/experiment"
	"variantsvc/internal/results"
)

// resultsBudget bounds the aggregation queries. Results are read by a
// human on the dashboard, never on the page path.
const resultsBudget = 10 * time.Second

// GET /v1/admin/experiments/{key}/results?goal=<goal>
func (s *Server) handleResults(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), resultsBudget)
	defer cancel()
	site := siteFrom(r)
	exp, err := s.Store.GetExperiment(ctx, site.ID, r.PathValue("key"))
	if err != nil {
		storeError(w, err, "experiment not found")
		return
	}
	goal := r.URL.Query().Get("goal")
	if goal != "" && !experiment.ValidKey(goal) {
		writeError(w, http.StatusBadRequest, "goal must be a key of lowercase letters, digits and hyphens")
		return
	}
	counts, unattributed, err := s.Store.ExperimentCounts(ctx, site.ID, exp.ID, goal)
	if err != nil {
		storeError(w, err, "")
		return
	}
	goals, err := s.Store.Goals(ctx, site.ID, exp.ID)
	if err != nil {
		storeError(w, err, "")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, results.Build(&exp, counts, unattributed, goals, goal, s.Now()))
}
