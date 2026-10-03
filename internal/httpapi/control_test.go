package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"variantsvc/internal/configcache"
	"variantsvc/internal/experiment"
	"variantsvc/internal/payload"
	"variantsvc/internal/site"
	"variantsvc/internal/store"
	"variantsvc/internal/store/storetest"
)

const testPlatformKey = "platform-test-key-0123456789"

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type dbServer struct {
	srv   *Server
	ts    *httptest.Server
	store *store.Store
	logs  *syncBuffer
}

// newDBServer wires a Server to an isolated database schema. Skips without
// TEST_DATABASE_URL.
func newDBServer(t *testing.T) *dbServer {
	t.Helper()
	st := storetest.Open(t)
	logs := &syncBuffer{}
	cache := &configcache.Cache{}
	src := configcache.DBSource{Store: st}
	if err := cache.Refresh(context.Background(), src, slog.Default()); err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		Cache: cache, Store: st, Source: src, PlatformKey: testPlatformKey,
		Log: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &dbServer{srv: srv, ts: ts, store: st, logs: logs}
}

// call performs a JSON request and decodes the response into out (if non-nil).
func (d *dbServer) call(t *testing.T, method, path, bearer string, body any, out any) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		if s, ok := body.(string); ok {
			rd = strings.NewReader(s)
		} else {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
	}
	req, _ := http.NewRequest(method, d.ts.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: bad JSON %q: %v", method, path, raw, err)
		}
	}
	return res
}

func (d *dbServer) createSite(t *testing.T, key string, origins ...string) (experiment.Site, string) {
	t.Helper()
	var resp siteWithKeyResponse
	res := d.call(t, "POST", "/v1/platform/sites", testPlatformKey, createSiteRequest{Key: key, Name: "Site " + key, AllowedOrigins: origins}, &resp)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create site %s: %d", key, res.StatusCode)
	}
	return resp.Site, resp.APIKey
}

func TestPlatformAuth(t *testing.T) {
	d := newDBServer(t)
	for _, bearer := range []string{"", "wrong", testPlatformKey + "x"} {
		res := d.call(t, "POST", "/v1/platform/sites", bearer, createSiteRequest{Key: "a"}, nil)
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("bearer %q: %d, want 401", bearer, res.StatusCode)
		}
	}
	d.srv.PlatformKey = ""
	if res := d.call(t, "GET", "/v1/platform/sites", "anything", nil, nil); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("disabled platform API: %d, want 503", res.StatusCode)
	}
}

func TestCreateSiteStoresOnlyHashAndNeverLogsKey(t *testing.T) {
	d := newDBServer(t)
	created, apiKey := d.createSite(t, "acme", "HTTPS://WWW.Acme.com/", "http://localhost:8080")
	if !strings.HasPrefix(apiKey, site.APIKeyPrefix) {
		t.Fatalf("api key %q", apiKey)
	}
	if created.AllowedOrigins[0] != "https://www.acme.com" {
		t.Errorf("origins not normalised: %v", created.AllowedOrigins)
	}
	stored, err := d.store.GetSite(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIKeyHash != site.HashKey(apiKey) || strings.Contains(stored.APIKeyHash, apiKey[10:30]) {
		t.Error("store must hold only the hash")
	}
	var ser bytes.Buffer
	json.NewEncoder(&ser).Encode(created)
	if strings.Contains(ser.String(), "api_key_hash") || strings.Contains(ser.String(), stored.APIKeyHash) {
		t.Error("hash must not be serialised in responses")
	}
	// Exercise an admin call so request logging runs, then check the logs.
	if res := d.call(t, "GET", "/v1/admin/site", apiKey, nil, nil); res.StatusCode != 200 {
		t.Fatalf("admin site with fresh key: %d (snapshot should refresh immediately)", res.StatusCode)
	}
	if strings.Contains(d.logs.String(), apiKey) {
		t.Fatal("API key appeared in logs")
	}
	// Validation.
	for name, body := range map[string]any{
		"bad key":     createSiteRequest{Key: "Acme!"},
		"bad origin":  createSiteRequest{Key: "ok", AllowedOrigins: []string{"acme.com"}},
		"unknown fld": `{"key":"ok","colour":"red"}`,
		"not json":    `{`,
	} {
		if res := d.call(t, "POST", "/v1/platform/sites", testPlatformKey, body, nil); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, res.StatusCode)
		}
	}
	if res := d.call(t, "POST", "/v1/platform/sites", testPlatformKey, createSiteRequest{Key: "acme"}, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("duplicate: %d, want 409", res.StatusCode)
	}
}

func TestSiteKeyAuthAndRotation(t *testing.T) {
	d := newDBServer(t)
	_, apiKey := d.createSite(t, "acme")
	for _, bearer := range []string{"", "garbage", "sk_live_notreal", strings.ToUpper(apiKey)} {
		if res := d.call(t, "GET", "/v1/admin/site", bearer, nil, nil); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("bearer %q: %d, want 401", bearer, res.StatusCode)
		}
	}
	var rotated siteWithKeyResponse
	if res := d.call(t, "POST", "/v1/platform/sites/acme/rotate-key", testPlatformKey, nil, &rotated); res.StatusCode != 200 || rotated.APIKey == apiKey {
		t.Fatalf("rotate: %d", res.StatusCode)
	}
	if res := d.call(t, "GET", "/v1/admin/site", apiKey, nil, nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("old key after rotation: %d, want 401", res.StatusCode)
	}
	if res := d.call(t, "GET", "/v1/admin/site", rotated.APIKey, nil, nil); res.StatusCode != 200 {
		t.Errorf("new key after rotation: %d", res.StatusCode)
	}
	if res := d.call(t, "POST", "/v1/platform/sites/ghost/rotate-key", testPlatformKey, nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("rotate unknown: %d", res.StatusCode)
	}
}

func TestSuspendDeleteAndPayload(t *testing.T) {
	d := newDBServer(t)
	created, apiKey := d.createSite(t, "acme")

	getPayload := func() payload.Payload {
		var p payload.Payload
		d.call(t, "GET", "/v1/sites/acme/payload.json", "", nil, &p)
		return p
	}
	if p := getPayload(); p.Version != created.PayloadVersion {
		t.Fatalf("payload version %d, site %d", p.Version, created.PayloadVersion)
	}
	var patched struct{ Site experiment.Site }
	if res := d.call(t, "PATCH", "/v1/platform/sites/acme", testPlatformKey, patchSiteRequest{Status: ptr("suspended")}, &patched); res.StatusCode != 200 {
		t.Fatalf("suspend: %d", res.StatusCode)
	}
	if p := getPayload(); p.Version != 0 || len(p.Experiments) != 0 {
		t.Errorf("suspended site payload must be empty: %+v", p)
	}
	if res := d.call(t, "GET", "/v1/admin/site", apiKey, nil, nil); res.StatusCode != 200 {
		t.Errorf("admin access while suspended should still work: %d", res.StatusCode)
	}
	if res := d.call(t, "PATCH", "/v1/platform/sites/acme", testPlatformKey, patchSiteRequest{Status: ptr("deleted")}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad status: %d", res.StatusCode)
	}
	d.call(t, "PATCH", "/v1/platform/sites/acme", testPlatformKey, patchSiteRequest{Status: ptr("active")}, nil)
	if p := getPayload(); p.Version <= created.PayloadVersion {
		t.Errorf("reactivated payload version %d should exceed %d", p.Version, created.PayloadVersion)
	}

	if res := d.call(t, "DELETE", "/v1/platform/sites/acme", testPlatformKey, nil, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	if res := d.call(t, "GET", "/v1/admin/site", apiKey, nil, nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("key of deleted site: %d, want 401", res.StatusCode)
	}
	if p := getPayload(); p.Version != 0 {
		t.Errorf("deleted site payload must be empty")
	}
	if res := d.call(t, "DELETE", "/v1/platform/sites/acme", testPlatformKey, nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("second delete: %d", res.StatusCode)
	}
}

func TestOwnSiteSettings(t *testing.T) {
	d := newDBServer(t)
	_, apiKey := d.createSite(t, "acme", "https://www.acme.com")
	var resp struct{ Site experiment.Site }
	res := d.call(t, "PATCH", "/v1/admin/site", apiKey, updateOwnSiteRequest{Name: ptr("Acme Inc"), AllowedOrigins: []string{"https://shop.acme.com:8443/", "https://shop.acme.com:8443"}}, &resp)
	if res.StatusCode != 200 || resp.Site.Name != "Acme Inc" || len(resp.Site.AllowedOrigins) != 1 || resp.Site.AllowedOrigins[0] != "https://shop.acme.com:8443" {
		t.Fatalf("patch: %d %+v", res.StatusCode, resp.Site)
	}
	res = d.call(t, "PATCH", "/v1/admin/site", apiKey, updateOwnSiteRequest{Name: ptr("Only name")}, &resp)
	if res.StatusCode != 200 || len(resp.Site.AllowedOrigins) != 1 {
		t.Fatalf("name-only patch must keep origins: %+v", resp.Site)
	}
	if res := d.call(t, "PATCH", "/v1/admin/site", apiKey, updateOwnSiteRequest{AllowedOrigins: []string{"nope"}}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad origin: %d", res.StatusCode)
	}
	if entry := d.srv.Cache.Get("acme"); entry == nil || len(entry.Site.AllowedOrigins) != 1 || entry.Site.Name != "Only name" {
		t.Error("snapshot must reflect settings immediately")
	}
}

func TestControlPlaneDisabledWithoutStore(t *testing.T) {
	_, ts := newTestServer(t, true)
	for _, p := range []string{"/v1/platform/sites", "/v1/admin/site"} {
		res, body := get(t, ts.URL+p, map[string]string{"Authorization": "Bearer x"})
		if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "DATABASE_URL") {
			t.Errorf("%s: %d %s", p, res.StatusCode, body)
		}
	}
}

func ptr(s string) *string { return &s }

func ctxBg() context.Context { return context.Background() }
