package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"variantsvc/web"
)

func TestSnippetServedWithBaseURL(t *testing.T) {
	s, ts := newTestServer(t, true)
	s.SnippetBaseURL = "https://cdn.example"
	res, body := get(t, ts.URL+"/v1/ab.js", nil)
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/javascript") {
		t.Fatalf("status %d type %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if res.Header.Get("Cache-Control") != snippetCacheControl {
		t.Errorf("Cache-Control %q", res.Header.Get("Cache-Control"))
	}
	if strings.Contains(string(body), web.BaseURLPlaceholder) || !strings.Contains(string(body), `'https://cdn.example'`) {
		t.Error("base URL placeholder not substituted")
	}
	if res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("snippet must be CORS-readable")
	}
	etag := res.Header.Get("ETag")
	if etag == "" {
		t.Fatal("snippet must carry an ETag so browsers can revalidate cheaply")
	}
	res2, body2 := get(t, ts.URL+"/v1/ab.js", map[string]string{"If-None-Match": etag})
	if res2.StatusCode != http.StatusNotModified || len(body2) != 0 {
		t.Errorf("If-None-Match: %d with %d bytes", res2.StatusCode, len(body2))
	}
}

func TestDemoPage(t *testing.T) {
	_, ts := newTestServer(t, true)
	for _, p := range []string{"/demo", "/demo/"} {
		res, body := get(t, ts.URL+p, nil)
		if res.StatusCode != 200 || !strings.Contains(string(body), `data-ab="hero-cta:headline"`) {
			t.Errorf("%s: status %d", p, res.StatusCode)
		}
	}
}
