package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"variantsvc/internal/assign"
	"variantsvc/internal/experiment"
)

// experimentView is the admin representation: the stored experiment plus
// the derived ranges and whether the payload would currently include it.
type experimentView struct {
	experiment.Experiment
	Ranges   [][2]int `json:"ranges"`
	Servable bool     `json:"servable"`
}

func view(e experiment.Experiment) experimentView {
	return experimentView{Experiment: e, Ranges: assign.Ranges(e.WeightsBP(), e.CoverageBP), Servable: e.Servable()}
}

type variantInput struct {
	Key       string         `json:"key"`
	WeightBP  int            `json:"weight_bp"`
	IsControl bool           `json:"is_control"`
	Content   map[string]any `json:"content"`
}

type createExperimentRequest struct {
	Key         string         `json:"key"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	URLPath     string         `json:"url_path"`
	CoverageBP  *int           `json:"coverage_bp"`
	Variants    []variantInput `json:"variants"`
}

func toVariants(in []variantInput) []experiment.Variant {
	out := make([]experiment.Variant, len(in))
	for i, v := range in {
		content := v.Content
		if content == nil {
			content = map[string]any{}
		}
		out[i] = experiment.Variant{Key: v.Key, WeightBP: v.WeightBP, IsControl: v.IsControl, Content: content,
			Source: experiment.SourceManual, Approved: true, Position: i}
	}
	return out
}

// POST /v1/admin/experiments
func (s *Server) handleCreateExperiment(w http.ResponseWriter, r *http.Request) {
	var req createExperimentRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	seed, err := experiment.NewSeed()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "seed generation failed")
		return
	}
	coverage := assign.Buckets
	if req.CoverageBP != nil {
		coverage = *req.CoverageBP
	}
	e := experiment.Experiment{
		Key: req.Key, Name: req.Name, Description: req.Description, Status: experiment.StatusDraft,
		Seed: seed, HashVersion: assign.HashVersion1, CoverageBP: coverage, URLPath: req.URLPath, Variants: toVariants(req.Variants),
	}
	if e.URLPath == "" {
		writeError(w, http.StatusBadRequest, "url_path is required: the one page this experiment runs on, for example \"/\" or \"/pricing.html\"")
		return
	}
	if err := e.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	e.URLPath, _ = experiment.NormalizeURLPath(e.URLPath)
	created, err := s.Store.CreateExperiment(r.Context(), siteFrom(r).ID, e)
	if err != nil {
		storeError(w, err, "experiment not found")
		return
	}
	s.refreshNow(r.Context())
	writeJSON(w, http.StatusCreated, map[string]any{"experiment": view(created)})
}

// GET /v1/admin/experiments
func (s *Server) handleListExperiments(w http.ResponseWriter, r *http.Request) {
	exps, err := s.Store.ListExperiments(r.Context(), siteFrom(r).ID)
	if err != nil {
		storeError(w, err, "")
		return
	}
	views := make([]experimentView, len(exps))
	for i, e := range exps {
		views[i] = view(e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"experiments": views})
}

// GET /v1/admin/experiments/{key}
func (s *Server) handleGetExperiment(w http.ResponseWriter, r *http.Request) {
	e, err := s.Store.GetExperiment(r.Context(), siteFrom(r).ID, r.PathValue("key"))
	if err != nil {
		storeError(w, err, "experiment not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"experiment": view(e)})
}

type patchExperimentRequest struct {
	Status      *experiment.Status `json:"status"`
	Name        *string            `json:"name"`
	Description *string            `json:"description"`
	URLPath     *string            `json:"url_path"`
	CoverageBP  *int               `json:"coverage_bp"`
	Variants    []variantInput     `json:"variants"`
}

// PATCH /v1/admin/experiments/{key}
//
// Product rules, returned as 409 with an explanation:
//   - status follows experiment.CanTransition; starting requires every
//     variant approved and a valid allocation;
//   - once an experiment has run, variants and weights are immutable and
//     coverage may only increase (visitors already assigned must not move);
//   - archived experiments are immutable.
func (s *Server) handlePatchExperiment(w http.ResponseWriter, r *http.Request) {
	var req patchExperimentRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	siteID := siteFrom(r).ID
	cur, err := s.Store.GetExperiment(r.Context(), siteID, r.PathValue("key"))
	if err != nil {
		storeError(w, err, "experiment not found")
		return
	}
	if cur.Status == experiment.StatusArchived {
		writeError(w, http.StatusConflict, "archived experiments are immutable; clone it as a new experiment")
		return
	}
	next := cur
	next.Variants = append([]experiment.Variant(nil), cur.Variants...)

	if req.Name != nil {
		next.Name = *req.Name
	}
	if req.Description != nil {
		next.Description = *req.Description
	}
	if req.URLPath != nil {
		// Moving an experiment to another page changes who enters it, never
		// which arm anyone is in, so it is allowed in any non-archived state.
		next.URLPath = *req.URLPath
	}
	if req.Variants != nil {
		if cur.Status != experiment.StatusDraft {
			writeError(w, http.StatusConflict, "weights and variants are immutable once an experiment has run, because changing them would move visitors between arms; pause it and create a new experiment (new seed) with the new split")
			return
		}
		next.Variants = toVariants(req.Variants)
	}
	if req.CoverageBP != nil {
		if cur.Status != experiment.StatusDraft && *req.CoverageBP < cur.CoverageBP {
			writeError(w, http.StatusConflict, fmt.Sprintf("coverage can only increase while an experiment is running or paused (currently %d bp); lowering it would drop visitors already assigned", cur.CoverageBP))
			return
		}
		next.CoverageBP = *req.CoverageBP
	}
	if req.Status != nil && *req.Status != cur.Status {
		if !req.Status.Valid() {
			writeError(w, http.StatusBadRequest, "status must be one of draft, running, paused, archived")
			return
		}
		if !experiment.CanTransition(cur.Status, *req.Status) {
			writeError(w, http.StatusConflict, fmt.Sprintf("cannot move from %s to %s", cur.Status, *req.Status))
			return
		}
		if *req.Status == experiment.StatusRunning {
			if blockers := next.StartBlockers(); len(blockers) > 0 {
				writeError(w, http.StatusConflict, "cannot start: "+strings.Join(blockers, "; "))
				return
			}
		}
		next.Status = *req.Status
	}
	if err := next.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	next.URLPath, _ = experiment.NormalizeURLPath(next.URLPath)
	saved, err := s.Store.SaveExperiment(r.Context(), siteID, next)
	if err != nil {
		storeError(w, err, "experiment not found")
		return
	}
	s.refreshNow(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"experiment": view(saved)})
}
