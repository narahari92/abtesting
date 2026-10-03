package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sync"

	"variantsvc/web"
)

const (
	// The snippet URL is not versioned, so browsers revalidate it every five
	// minutes with If-None-Match (a cheap 304) while a CDN keeps serving it
	// stale for a day. A fix therefore reaches customer pages within minutes
	// instead of being pinned by a long max-age.
	snippetCacheControl = "public, max-age=300, stale-while-revalidate=86400"
	pageCacheControl    = "public, max-age=60"
)

// snippet is the served ab.js with the base URL substituted, built once.
type snippet struct {
	once sync.Once
	body []byte
	etag string
	err  error
}

func (s *Server) snippetBody() ([]byte, string, error) {
	s.snippet.once.Do(func() {
		src, err := web.Files.ReadFile("ab.js")
		if err != nil {
			s.snippet.err = err
			return
		}
		s.snippet.body = bytes.ReplaceAll(src, []byte(web.BaseURLPlaceholder), []byte(s.SnippetBaseURL))
		sum := sha256.Sum256(s.snippet.body)
		s.snippet.etag = `"` + hex.EncodeToString(sum[:8]) + `"`
	})
	return s.snippet.body, s.snippet.etag, s.snippet.err
}

// handleSnippet serves ab.js with the payload base URL substituted, so an
// inline or URL install works without the customer configuring a host.
func (s *Server) handleSnippet(w http.ResponseWriter, r *http.Request) {
	body, etag, err := s.snippetBody()
	if err != nil {
		http.Error(w, "snippet unavailable", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/javascript; charset=utf-8")
	h.Set("Cache-Control", snippetCacheControl)
	h.Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(body)
}

func (s *Server) handleDemo(w http.ResponseWriter, _ *http.Request) {
	page, err := web.Files.ReadFile("demo/index.html")
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", pageCacheControl)
	w.Write(page)
}
