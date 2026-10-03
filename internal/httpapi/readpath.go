package httpapi

import (
	"net/http"
	"strings"

	"variantsvc/internal/assign"
	"variantsvc/internal/experiment"
	"variantsvc/internal/payload"
)

// handlePayload serves the compiled per-site payload. It never returns an
// error status: an unknown, suspended or not-yet-loaded site gets an empty
// payload with the same headers, so the snippet's behaviour is identical
// and the CDN can cache the answer.
func (s *Server) handlePayload(w http.ResponseWriter, r *http.Request) {
	site := r.PathValue("site")
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", PayloadCacheControl)

	var body []byte
	var etag string
	if entry := s.Cache.Get(site); entry != nil && experiment.ValidKey(site) {
		body, etag = entry.Bytes, entry.ETag
	} else {
		empty := payload.Empty(site)
		body, _ = empty.Marshal()
		etag = empty.ETag()
	}
	h.Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(body)
}

// etagMatches implements the If-None-Match comparison for a list of tags,
// tolerating weak validators added by intermediaries.
func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

type assignResponse struct {
	Assignments []payload.Assignment `json:"assignments"`
}

// handleAssign is the server-side evaluator for callers that cannot run
// the browser snippet. Any invalid input yields an empty assignment list
// with status 200: the hot path never returns an error status.
func (s *Server) handleAssign(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", AssignCacheControl)
	q := r.URL.Query()
	site, visitor := q.Get("site"), q.Get("v")
	resp := assignResponse{Assignments: []payload.Assignment{}}

	if !experiment.ValidKey(site) || !assign.ValidVisitorID(visitor) {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	entry := s.Cache.Get(site)
	if entry == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	var keys []string
	if e := q.Get("e"); e != "" {
		for _, k := range strings.Split(e, ",") {
			if k = strings.TrimSpace(k); experiment.ValidKey(k) {
				keys = append(keys, k)
			}
		}
		if len(keys) == 0 {
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}
	resp.Assignments = entry.Payload.Evaluate(visitor, keys)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("ok\n"))
}

type readyResponse struct {
	Status           string  `json:"status"`
	SnapshotLoaded   bool    `json:"snapshot_loaded"`
	ConfigAgeSeconds float64 `json:"config_age_seconds"`
	Sites            int     `json:"sites"`
}

// handleReadyz reports snapshot state. It returns 503 only before the first
// successful load; afterwards a stale snapshot is still "ready", because
// pulling replicas out of rotation during a config-store outage would turn
// a degraded control plane into a down data plane.
func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	snap := s.Cache.Current()
	if snap == nil {
		writeJSON(w, http.StatusServiceUnavailable, readyResponse{Status: "starting"})
		return
	}
	writeJSON(w, http.StatusOK, readyResponse{
		Status:           "ok",
		SnapshotLoaded:   true,
		ConfigAgeSeconds: s.Now().Sub(snap.LoadedAt).Seconds(),
		Sites:            len(snap.Sites),
	})
}
