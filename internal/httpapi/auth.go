package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"variantsvc/internal/experiment"
	"variantsvc/internal/site"
	"variantsvc/internal/store"
)

// maxBodyBytes bounds every JSON request body.
const maxBodyBytes = 64 << 10

type ctxKey int

const siteCtxKey ctxKey = iota

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// requirePlatform guards operator endpoints with the platform admin key.
func (s *Server) requirePlatform(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.PlatformKey == "" {
			writeError(w, http.StatusServiceUnavailable, "platform API disabled: PLATFORM_ADMIN_KEY is not set")
			return
		}
		if !site.KeysEqual(bearerToken(r), s.PlatformKey) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="platform"`)
			writeError(w, http.StatusUnauthorized, "invalid platform key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireSite resolves a Bearer site API key to its site and stores it in
// the request context. The snapshot answers without I/O; the store is
// consulted only when the snapshot does not know the key yet, which
// happens for a few seconds after a site is created on another replica.
func (s *Server) requireSite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" || !strings.HasPrefix(token, site.APIKeyPrefix) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="site"`)
			writeError(w, http.StatusUnauthorized, "missing or malformed site API key")
			return
		}
		hash := site.HashKey(token)
		var st experiment.Site
		if entry := s.Cache.GetByAPIKeyHash(hash); entry != nil {
			st = entry.Site
		} else if s.Store != nil {
			found, err := s.Store.GetSiteByAPIKeyHash(r.Context(), hash)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusServiceUnavailable, "authentication store unavailable")
				return
			}
			st = found
		}
		if st.ID == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="site"`)
			writeError(w, http.StatusUnauthorized, "invalid site API key")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), siteCtxKey, st)))
	})
}

// siteFrom returns the authenticated site placed by requireSite.
func siteFrom(r *http.Request) experiment.Site {
	st, _ := r.Context().Value(siteCtxKey).(experiment.Site)
	return st
}

// decodeJSON reads a bounded JSON body into v, rejecting unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "cannot read body")
		return false
	}
	if len(body) > maxBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "body too large")
		return false
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// storeError maps store sentinels to HTTP statuses.
func storeError(w http.ResponseWriter, err error, notFoundMsg string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, notFoundMsg)
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "already exists")
	default:
		writeError(w, http.StatusServiceUnavailable, "storage unavailable")
	}
}
