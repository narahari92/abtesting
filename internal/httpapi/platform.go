package httpapi

import (
	"net/http"

	"variantsvc/internal/experiment"
	"variantsvc/internal/site"
)

type createSiteRequest struct {
	Key            string   `json:"key"`
	Name           string   `json:"name"`
	AllowedOrigins []string `json:"allowed_origins"`
}

type siteWithKeyResponse struct {
	Site   experiment.Site `json:"site"`
	APIKey string          `json:"api_key"`
	Note   string          `json:"note"`
}

const apiKeyNote = "Store this key now. It is shown once and only its hash is kept."

// normalizeOrigins validates and canonicalises an allow-list.
func normalizeOrigins(raw []string) ([]string, string) {
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, o := range raw {
		n, ok := site.NormalizeOrigin(o)
		if !ok {
			return nil, "allowed_origins entry " + o + " is not a plain http(s) origin such as https://www.example.com"
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) > 50 {
		return nil, "at most 50 allowed origins"
	}
	return out, ""
}

// POST /v1/platform/sites
func (s *Server) handleCreateSite(w http.ResponseWriter, r *http.Request) {
	var req createSiteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !experiment.ValidKey(req.Key) {
		writeError(w, http.StatusBadRequest, "key must be 1..64 lowercase letters, digits or hyphens")
		return
	}
	if len(req.Name) > experiment.MaxNameLen {
		writeError(w, http.StatusBadRequest, "name too long")
		return
	}
	origins, msg := normalizeOrigins(req.AllowedOrigins)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	apiKey, err := site.NewAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "key generation failed")
		return
	}
	created, err := s.Store.CreateSite(r.Context(), experiment.Site{Key: req.Key, Name: req.Name, APIKeyHash: site.HashKey(apiKey), AllowedOrigins: origins})
	if err != nil {
		storeError(w, err, "site not found")
		return
	}
	s.refreshNow(r.Context())
	writeJSON(w, http.StatusCreated, siteWithKeyResponse{Site: created, APIKey: apiKey, Note: apiKeyNote})
}

// GET /v1/platform/sites
func (s *Server) handleListSites(w http.ResponseWriter, r *http.Request) {
	sites, err := s.Store.ListSites(r.Context())
	if err != nil {
		storeError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": sites})
}

// POST /v1/platform/sites/{key}/rotate-key
func (s *Server) handleRotateKey(w http.ResponseWriter, r *http.Request) {
	apiKey, err := site.NewAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "key generation failed")
		return
	}
	updated, err := s.Store.RotateSiteKey(r.Context(), r.PathValue("key"), site.HashKey(apiKey))
	if err != nil {
		storeError(w, err, "site not found")
		return
	}
	s.refreshNow(r.Context())
	writeJSON(w, http.StatusOK, siteWithKeyResponse{Site: updated, APIKey: apiKey, Note: apiKeyNote})
}

type patchSiteRequest struct {
	Status *string `json:"status"`
}

// PATCH /v1/platform/sites/{key}  {"status": "active" | "suspended"}
func (s *Server) handlePatchSite(w http.ResponseWriter, r *http.Request) {
	var req patchSiteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Status == nil || (*req.Status != experiment.SiteActive && *req.Status != experiment.SiteSuspended) {
		writeError(w, http.StatusBadRequest, "status must be active or suspended")
		return
	}
	updated, err := s.Store.SetSiteStatus(r.Context(), r.PathValue("key"), *req.Status)
	if err != nil {
		storeError(w, err, "site not found")
		return
	}
	s.refreshNow(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"site": updated})
}

// DELETE /v1/platform/sites/{key}
func (s *Server) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteSite(r.Context(), r.PathValue("key")); err != nil {
		storeError(w, err, "site not found")
		return
	}
	s.refreshNow(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// GET /v1/admin/site
func (s *Server) handleGetOwnSite(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.GetSite(r.Context(), siteFrom(r).Key)
	if err != nil {
		storeError(w, err, "site not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"site": st})
}

type updateOwnSiteRequest struct {
	Name           *string  `json:"name"`
	AllowedOrigins []string `json:"allowed_origins"`
}

// PATCH /v1/admin/site  {"name": "...", "allowed_origins": [...]}
func (s *Server) handleUpdateOwnSite(w http.ResponseWriter, r *http.Request) {
	var req updateOwnSiteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	current, err := s.Store.GetSite(r.Context(), siteFrom(r).Key)
	if err != nil {
		storeError(w, err, "site not found")
		return
	}
	name := current.Name
	if req.Name != nil {
		if len(*req.Name) > experiment.MaxNameLen {
			writeError(w, http.StatusBadRequest, "name too long")
			return
		}
		name = *req.Name
	}
	origins := current.AllowedOrigins
	if req.AllowedOrigins != nil {
		var msg string
		if origins, msg = normalizeOrigins(req.AllowedOrigins); msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
	}
	updated, err := s.Store.UpdateSiteSettings(r.Context(), current.Key, name, origins)
	if err != nil {
		storeError(w, err, "site not found")
		return
	}
	s.refreshNow(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"site": updated})
}
