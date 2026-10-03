package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"variantsvc/internal/configcache"
	"variantsvc/internal/events"
	"variantsvc/internal/site"
	"variantsvc/internal/store"
)

// Budgets for the memory-only hot path. They are context deadlines, not
// response timeouts: a browser never receives an error status from these
// handlers, so a blown budget would surface in logs, not on a page.
const (
	HotPathBudget = 50 * time.Millisecond
	// PayloadCacheControl is what lets a CDN and the browser serve the
	// payload without touching the origin, and keep serving it when the
	// origin is down.
	PayloadCacheControl = "public, max-age=30, stale-while-revalidate=3600, stale-if-error=36000"
	AssignCacheControl  = "private, max-age=30"
)

// Server holds the dependencies of all handlers.
type Server struct {
	Cache *configcache.Cache
	Log   *slog.Logger
	// Now is injectable for tests.
	Now func() time.Time
	// SnippetBaseURL is substituted into ab.js as the host to fetch payloads
	// from. Empty means same-origin relative URLs.
	SnippetBaseURL string

	// Store is nil when running from a config file; the control plane is
	// then disabled and answers 503.
	Store *store.Store
	// Source lets control-plane writes refresh this replica's snapshot
	// immediately instead of waiting for the next poll.
	Source configcache.Source
	// PlatformKey guards /v1/platform/*. Empty disables those endpoints.
	PlatformKey string
	// Limiter holds the per-site token buckets for events and assign.
	Limiter *site.Limiter
	// Events is built in Handler from Cache, Store, Limiter and Log.
	Events *events.Recorder

	snippet snippet
}

// Handler builds the routed, middleware-wrapped http.Handler.
func (s *Server) Handler() http.Handler {
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.Log == nil {
		s.Log = slog.Default()
	}
	if s.Limiter == nil {
		s.Limiter = site.NewLimiter()
	}
	if s.Events == nil {
		s.Events = &events.Recorder{Cache: s.Cache, Store: s.Store, Limiter: s.Limiter, Log: s.Log}
	}
	mux := http.NewServeMux()

	mux.Handle("GET /v1/sites/{site}/payload.json", s.budget(HotPathBudget, http.HandlerFunc(s.handlePayload)))
	mux.Handle("GET /v1/assign", s.budget(HotPathBudget, http.HandlerFunc(s.handleAssign)))
	// The write path is registered even without a store: it then drops
	// every event with 202, which is what a browser must always receive.
	mux.Handle("POST /v1/events/exposure", s.budget(events.WriteBudget+time.Second, http.HandlerFunc(s.handleExposure)))
	mux.Handle("POST /v1/events/conversion", s.budget(events.WriteBudget+time.Second, http.HandlerFunc(s.handleConversion)))
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /v1/ab.js", s.handleSnippet)
	mux.HandleFunc("GET /demo", s.handleDemo)
	mux.HandleFunc("GET /demo/{$}", s.handleDemo)

	if s.Store == nil {
		mux.HandleFunc("/v1/platform/", s.controlPlaneDisabled)
		mux.HandleFunc("/v1/admin/", s.controlPlaneDisabled)
	} else {
		platform := s.requirePlatform
		mux.Handle("POST /v1/platform/sites", platform(http.HandlerFunc(s.handleCreateSite)))
		mux.Handle("GET /v1/platform/sites", platform(http.HandlerFunc(s.handleListSites)))
		mux.Handle("POST /v1/platform/sites/{key}/rotate-key", platform(http.HandlerFunc(s.handleRotateKey)))
		mux.Handle("PATCH /v1/platform/sites/{key}", platform(http.HandlerFunc(s.handlePatchSite)))
		mux.Handle("DELETE /v1/platform/sites/{key}", platform(http.HandlerFunc(s.handleDeleteSite)))

		admin := s.requireSite
		mux.Handle("GET /v1/admin/site", admin(http.HandlerFunc(s.handleGetOwnSite)))
		mux.Handle("PATCH /v1/admin/site", admin(http.HandlerFunc(s.handleUpdateOwnSite)))
		mux.Handle("POST /v1/admin/experiments", admin(http.HandlerFunc(s.handleCreateExperiment)))
		mux.Handle("GET /v1/admin/experiments", admin(http.HandlerFunc(s.handleListExperiments)))
		mux.Handle("GET /v1/admin/experiments/{key}", admin(http.HandlerFunc(s.handleGetExperiment)))
		mux.Handle("PATCH /v1/admin/experiments/{key}", admin(http.HandlerFunc(s.handlePatchExperiment)))
	}

	return s.recoverer(s.logging(cors(mux)))
}

// budget attaches a context deadline to the request.
func (s *Server) budget(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// cors makes every endpoint callable from any customer origin. No
// credentials are ever used, so "*" is correct and keeps GET and
// sendBeacon requests "simple" (no preflight). Preflights that do arrive
// (dashboard PATCH, JSON POSTs) are answered without touching a handler.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-None-Match")
			h.Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		s.Log.Info("request",
			"method", r.Method, "path", r.URL.Path, "status", sw.status,
			"bytes", sw.bytes, "duration_ms", float64(s.Now().Sub(start).Microseconds())/1000)
	})
}

// recoverer turns a panic into a logged 500 instead of a dropped
// connection. Hot-path handlers are written not to panic; this is the last
// line of defence.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.Log.Error("panic in handler", "path", r.URL.Path, "panic", rec)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// controlPlaneDisabled answers for admin and platform routes when no
// database is configured.
func (s *Server) controlPlaneDisabled(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusServiceUnavailable, "control plane disabled: the service is running from a config file without DATABASE_URL")
}

// refreshNow rebuilds this replica's snapshot after a control-plane write
// so the change is visible here immediately. Other replicas pick it up on
// their next poll. Failure is logged, never surfaced: the write succeeded.
func (s *Server) refreshNow(ctx context.Context) {
	if s.Source == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.Cache.Refresh(ctx, s.Source, s.Log); err != nil {
		s.Log.Warn("snapshot refresh after write failed", "err", err)
	}
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
