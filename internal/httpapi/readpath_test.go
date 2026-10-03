package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"variantsvc/internal/assign"
	"variantsvc/internal/configcache"
	"variantsvc/internal/payload"
)

func newTestServer(t *testing.T, loaded bool) (*Server, *httptest.Server) {
	t.Helper()
	cache := &configcache.Cache{}
	if loaded {
		src := configcache.FileSource{Path: "../configcache/testdata/config.sample.json"}
		if err := cache.Refresh(context.Background(), src, slog.Default()); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{Cache: cache, Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func get(t *testing.T, url string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, body
}

func TestPayloadHeadersAndBody(t *testing.T) {
	s, ts := newTestServer(t, true)
	res, body := get(t, ts.URL+"/v1/sites/demo/payload.json", nil)
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	if cc := res.Header.Get("Cache-Control"); cc != PayloadCacheControl {
		t.Errorf("Cache-Control = %q", cc)
	}
	if res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS header")
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	entry := s.Cache.Get("demo")
	if res.Header.Get("ETag") != entry.ETag {
		t.Errorf("ETag %q != %q", res.Header.Get("ETag"), entry.ETag)
	}
	if string(body) != string(entry.Bytes) {
		t.Errorf("body differs from snapshot bytes")
	}
	var p payload.Payload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Site != "demo" || len(p.Experiments) != 2 || p.Find("hero-cta") == nil {
		t.Errorf("unexpected payload: %+v", p)
	}
	if r := p.Find("checkout-flow").Ranges; r[0] != [2]int{0, 4500} || r[1] != [2]int{5000, 9500} {
		t.Errorf("ranges %v", r)
	}
}

func TestPayloadETagRoundTrip(t *testing.T) {
	_, ts := newTestServer(t, true)
	res, _ := get(t, ts.URL+"/v1/sites/demo/payload.json", nil)
	etag := res.Header.Get("ETag")
	for _, inm := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
		res2, body := get(t, ts.URL+"/v1/sites/demo/payload.json", map[string]string{"If-None-Match": inm})
		if res2.StatusCode != http.StatusNotModified || len(body) != 0 {
			t.Errorf("If-None-Match %q: status %d body %d bytes", inm, res2.StatusCode, len(body))
		}
		if res2.Header.Get("Cache-Control") != PayloadCacheControl {
			t.Error("304 must carry Cache-Control")
		}
	}
	res3, _ := get(t, ts.URL+"/v1/sites/demo/payload.json", map[string]string{"If-None-Match": `"stale"`})
	if res3.StatusCode != 200 {
		t.Errorf("mismatched tag should return 200, got %d", res3.StatusCode)
	}
}

func TestPayloadUnknownSuspendedAndUnloaded(t *testing.T) {
	cases := []struct {
		name   string
		loaded bool
		site   string
	}{
		{"unknown", true, "ghost"},
		{"suspended", true, "suspended-site"},
		{"bad key", true, "Not%20A%20Key"},
		{"before first load", false, "demo"},
	}
	for _, c := range cases {
		_, ts := newTestServer(t, c.loaded)
		res, body := get(t, ts.URL+"/v1/sites/"+c.site+"/payload.json", nil)
		if res.StatusCode != 200 {
			t.Errorf("%s: status %d", c.name, res.StatusCode)
		}
		if res.Header.Get("Cache-Control") != PayloadCacheControl || res.Header.Get("ETag") == "" {
			t.Errorf("%s: headers missing", c.name)
		}
		var p payload.Payload
		if err := json.Unmarshal(body, &p); err != nil || len(p.Experiments) != 0 {
			t.Errorf("%s: expected empty experiments, got %s (%v)", c.name, body, err)
		}
	}
}

func TestAssignMatchesReference(t *testing.T) {
	s, ts := newTestServer(t, true)
	res, body := get(t, ts.URL+"/v1/assign?site=demo&v=visitor-8841", nil)
	if res.StatusCode != 200 || res.Header.Get("Cache-Control") != AssignCacheControl {
		t.Fatalf("status %d, cache-control %q", res.StatusCode, res.Header.Get("Cache-Control"))
	}
	var got assignResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	p := s.Cache.Get("demo").Payload
	hero := p.Find("hero-cta")
	idx := assign.Evaluate(hero.Seed, "visitor-8841", hero.HashVersion, hero.Ranges)
	var found bool
	for _, a := range got.Assignments {
		if a.Experiment == "hero-cta" {
			found = true
			if a.Variant != hero.Variants[idx].Key {
				t.Errorf("variant %q, reference %q", a.Variant, hero.Variants[idx].Key)
			}
			if a.Content["headline"] == nil {
				t.Error("content missing")
			}
		}
	}
	if !found {
		t.Fatalf("hero-cta missing (coverage is 100%%): %s", body)
	}
	// Stable across calls.
	_, body2 := get(t, ts.URL+"/v1/assign?site=demo&v=visitor-8841", nil)
	if string(body) != string(body2) {
		t.Error("assign not stable")
	}
}

func TestAssignFilterAndFailOpen(t *testing.T) {
	_, ts := newTestServer(t, true)
	decode := func(b []byte) assignResponse {
		var r assignResponse
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		if r.Assignments == nil {
			t.Fatalf("assignments must be an array, got %s", b)
		}
		return r
	}
	_, body := get(t, ts.URL+"/v1/assign?site=demo&v=visitor-8841&e=hero-cta,nope", nil)
	if r := decode(body); len(r.Assignments) != 1 || r.Assignments[0].Experiment != "hero-cta" {
		t.Errorf("filter: %s", body)
	}
	for _, q := range []string{"", "site=demo", "v=x", "site=ghost&v=x", "site=demo&v=", "site=demo&v=" + strings.Repeat("x", 129), "site=demo&v=ok&e=BAD", "site=Bad&v=x"} {
		res, body := get(t, ts.URL+"/v1/assign?"+q, nil)
		if res.StatusCode != 200 {
			t.Errorf("%q: status %d", q, res.StatusCode)
		}
		if r := decode(body); len(r.Assignments) != 0 {
			t.Errorf("%q: expected empty, got %s", q, body)
		}
	}
}

func TestHealthAndReady(t *testing.T) {
	_, ts := newTestServer(t, false)
	res, _ := get(t, ts.URL+"/healthz", nil)
	if res.StatusCode != 200 {
		t.Errorf("healthz %d", res.StatusCode)
	}
	res, _ = get(t, ts.URL+"/readyz", nil)
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("readyz before load = %d, want 503", res.StatusCode)
	}

	s, ts2 := newTestServer(t, true)
	s.Now = func() time.Time { return s.Cache.Current().LoadedAt.Add(42 * time.Second) }
	res, body := get(t, ts2.URL+"/readyz", nil)
	var rr readyResponse
	json.Unmarshal(body, &rr)
	if res.StatusCode != 200 || !rr.SnapshotLoaded || rr.Sites != 2 || rr.ConfigAgeSeconds < 41 {
		t.Errorf("readyz after load: %d %s", res.StatusCode, body)
	}
}

func TestCORSPreflight(t *testing.T) {
	_, ts := newTestServer(t, true)
	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/v1/assign", nil)
	req.Header.Set("Origin", "https://www.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusNoContent || res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("preflight: %d %v", res.StatusCode, res.Header)
	}
}
