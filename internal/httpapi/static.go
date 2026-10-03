package httpapi

import (
	"bytes"
	"net/http"

	"variantsvc/web"
)

const (
	snippetCacheControl = "public, max-age=86400, stale-while-revalidate=604800"
	pageCacheControl    = "public, max-age=60"
)

// handleSnippet serves ab.js with the payload base URL substituted, so an
// inline or URL install works without the customer configuring a host.
func (s *Server) handleSnippet(w http.ResponseWriter, _ *http.Request) {
	src, err := web.Files.ReadFile("ab.js")
	if err != nil {
		http.Error(w, "snippet unavailable", http.StatusInternalServerError)
		return
	}
	body := bytes.ReplaceAll(src, []byte(web.BaseURLPlaceholder), []byte(s.SnippetBaseURL))
	h := w.Header()
	h.Set("Content-Type", "application/javascript; charset=utf-8")
	h.Set("Cache-Control", snippetCacheControl)
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
