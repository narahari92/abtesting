package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"variantsvc/internal/events"
	"variantsvc/internal/experiment"
	"variantsvc/internal/store"
)

const acmeOrigin = "https://www.acme.com"

// beacon posts a raw body with optional Origin and Bearer, like sendBeacon
// (text/plain) or a server integration would.
func (d *dbServer) beacon(t *testing.T, path, body, origin, bearer string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, d.ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// trackingSite creates a site with an allowed origin and a running
// experiment, returning the site key, API key and experiment id.
func trackingSite(t *testing.T, d *dbServer) (string, string, string) {
	t.Helper()
	_, apiKey := d.createSite(t, "acme", acmeOrigin)
	d.createExperiment(t, apiKey, heroRequest())
	running := experiment.StatusRunning
	if res, _, er := d.patch(t, apiKey, "hero-cta", patchExperimentRequest{Status: &running}); res.StatusCode != 200 {
		t.Fatalf("start: %d %s", res.StatusCode, er.Error)
	}
	exp := d.srv.Cache.Get("acme").Experiments["hero-cta"]
	if exp == nil || exp.ID == "" {
		t.Fatal("snapshot must index the experiment with its id")
	}
	return "acme", apiKey, exp.ID
}

func (d *dbServer) counts(t *testing.T, expID string) store.EventCounts {
	t.Helper()
	c, err := d.store.CountEvents(ctxBg(), expID)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestExposureIdempotentAndOriginAuth(t *testing.T) {
	d := newDBServer(t)
	_, _, expID := trackingSite(t, d)
	body := `{"site":"acme","v":"visitor-1","experiment":"hero-cta","variant":"b"}`

	if code := d.beacon(t, "/v1/events/exposure", body, acmeOrigin, ""); code != http.StatusAccepted {
		t.Fatalf("status %d", code)
	}
	if c := d.counts(t, expID); c.Exposures != 1 {
		t.Fatalf("exposures = %d, want 1", c.Exposures)
	}
	// Duplicate, and a different variant for the same visitor: first write wins.
	d.beacon(t, "/v1/events/exposure", body, acmeOrigin, "")
	d.beacon(t, "/v1/events/exposure", strings.Replace(body, `"b"`, `"control"`, 1), acmeOrigin, "")
	if c := d.counts(t, expID); c.Exposures != 1 {
		t.Fatalf("exposures after duplicates = %d, want 1", c.Exposures)
	}
	// Another visitor adds a row. Origin matching is normalised.
	d.beacon(t, "/v1/events/exposure", strings.Replace(body, "visitor-1", "visitor-2", 1), "HTTPS://WWW.ACME.COM:443", "")
	if c := d.counts(t, expID); c.Exposures != 2 {
		t.Fatalf("exposures = %d, want 2", c.Exposures)
	}
	// Wrong, missing or forged-looking origins: 202 and nothing written.
	for _, origin := range []string{"https://evil.com", "", "null", "https://acme.com", "http://www.acme.com"} {
		if code := d.beacon(t, "/v1/events/exposure", strings.Replace(body, "visitor-1", "visitor-x", 1), origin, ""); code != http.StatusAccepted {
			t.Errorf("origin %q: status %d", origin, code)
		}
	}
	if c := d.counts(t, expID); c.Exposures != 2 {
		t.Fatalf("bad origins must not write: exposures = %d", c.Exposures)
	}
}

func TestExposureAPIKeyAuth(t *testing.T) {
	d := newDBServer(t)
	_, apiKey, expID := trackingSite(t, d)
	_, otherKey := d.createSite(t, "other", "https://other.example")
	body := `{"site":"acme","v":"server-user-9","experiment":"hero-cta","variant":"control"}`

	if code := d.beacon(t, "/v1/events/exposure", body, "", apiKey); code != http.StatusAccepted {
		t.Fatalf("status %d", code)
	}
	if c := d.counts(t, expID); c.Exposures != 1 {
		t.Fatalf("API key without Origin must write: %d", c.Exposures)
	}
	// A valid key for another tenant, or a garbage key, writes nothing even with a good Origin.
	d.beacon(t, "/v1/events/exposure", strings.Replace(body, "user-9", "user-10", 1), acmeOrigin, otherKey)
	d.beacon(t, "/v1/events/exposure", strings.Replace(body, "user-9", "user-11", 1), acmeOrigin, "sk_live_garbage")
	if c := d.counts(t, expID); c.Exposures != 1 {
		t.Fatalf("foreign or bad key must not write: %d", c.Exposures)
	}
}

func TestExposureValidation(t *testing.T) {
	d := newDBServer(t)
	_, _, expID := trackingSite(t, d)
	bad := []string{
		`{"site":"acme","v":"v1","experiment":"nope","variant":"b"}`,        // unknown experiment
		`{"site":"acme","v":"v1","experiment":"hero-cta","variant":"zzz"}`,  // unknown variant
		`{"site":"acme","v":"","experiment":"hero-cta","variant":"b"}`,      // empty visitor
		`{"site":"acme","v":"café","experiment":"hero-cta","variant":"b"}`,  // non-ASCII visitor
		`{"site":"ghost","v":"v1","experiment":"hero-cta","variant":"b"}`,   // unknown site
		`{"site":"Bad Key","v":"v1","experiment":"hero-cta","variant":"b"}`, // malformed site
		`not json`,
		``,
		`{"site":"acme","v":"v1","experiment":"hero-cta","variant":"b","pad":"` + strings.Repeat("x", eventBodyLimit) + `"}`, // oversized
	}
	for _, b := range bad {
		if code := d.beacon(t, "/v1/events/exposure", b, acmeOrigin, ""); code != http.StatusAccepted {
			t.Errorf("body %.40q: status %d, want 202", b, code)
		}
	}
	if c := d.counts(t, expID); c.Exposures != 0 {
		t.Fatalf("invalid beacons must not write: %d", c.Exposures)
	}
}

func TestConversionIdempotentPerGoalAndAfterPause(t *testing.T) {
	d := newDBServer(t)
	_, apiKey, expID := trackingSite(t, d)
	conv := func(v, goal, extra string) {
		d.beacon(t, "/v1/events/conversion", `{"site":"acme","v":"`+v+`","experiment":"hero-cta","goal":"`+goal+`"`+extra+`}`, acmeOrigin, "")
	}
	conv("visitor-1", "signup", "")
	conv("visitor-1", "signup", "")
	conv("visitor-1", "checkout", `,"value":49`)
	if c := d.counts(t, expID); c.Conversions != 2 {
		t.Fatalf("conversions = %d, want 2 (one per goal)", c.Conversions)
	}
	var value *float64
	if err := d.store.Pool().QueryRow(ctxBg(), `SELECT value FROM conversions WHERE experiment_id=$1 AND goal='checkout'`, expID).Scan(&value); err != nil || value == nil || *value != 49 {
		t.Fatalf("value = %v, err %v", value, err)
	}
	// A client-supplied variant is ignored, not stored.
	conv("visitor-2", "signup", `,"variant":"b"`)
	if c := d.counts(t, expID); c.Conversions != 3 {
		t.Fatalf("conversion with stray variant should still be accepted: %d", c.Conversions)
	}
	// Bad goals and values are dropped.
	conv("visitor-3", "Sign Up!", "")
	conv("visitor-3", "signup", `,"value":1e300`)
	conv("visitor-3", "signup", `,"value":"ten"`)
	if c := d.counts(t, expID); c.Conversions != 3 {
		t.Fatalf("bad goal/value must not write: %d", c.Conversions)
	}
	// Conversions keep flowing after the experiment is paused.
	paused := experiment.StatusPaused
	d.patch(t, apiKey, "hero-cta", patchExperimentRequest{Status: &paused})
	conv("visitor-4", "signup", "")
	if c := d.counts(t, expID); c.Conversions != 4 {
		t.Fatalf("conversion after pause must be recorded: %d", c.Conversions)
	}
}

func TestEventsDroppedWhenSuspendedRateLimitedOrNoStore(t *testing.T) {
	d := newDBServer(t)
	_, _, expID := trackingSite(t, d)
	body := func(v string) string {
		return `{"site":"acme","v":"` + v + `","experiment":"hero-cta","variant":"b"}`
	}

	// Rate limit: shrink the site's limit in the snapshot (2 rps, burst 4).
	d.srv.Cache.Get("acme").Site.EventsRPSLimit = 2
	for i := 0; i < 20; i++ {
		d.beacon(t, "/v1/events/exposure", body("rl-"+string(rune('a'+i))), acmeOrigin, "")
	}
	if c := d.counts(t, expID); c.Exposures == 0 || c.Exposures > 4 {
		t.Fatalf("rate limited: %d rows, want 1..4", c.Exposures)
	}
	before := d.counts(t, expID).Exposures

	// Suspended site: dropped.
	d.call(t, "PATCH", "/v1/platform/sites/acme", testPlatformKey, patchSiteRequest{Status: ptr("suspended")}, nil)
	if code := d.beacon(t, "/v1/events/exposure", body("suspended-1"), acmeOrigin, ""); code != http.StatusAccepted {
		t.Fatalf("status %d", code)
	}
	if c := d.counts(t, expID); c.Exposures != before {
		t.Fatal("suspended site must not record events")
	}

	// No store (file mode): 202, nothing to write to.
	_, ts := newTestServer(t, true)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/events/exposure", strings.NewReader(`{"site":"demo","v":"v","experiment":"hero-cta","variant":"b"}`))
	req.Header.Set("Origin", "http://localhost:8080")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("no-store server: %d", res.StatusCode)
	}
}

func TestEventsStoreOutageStill202(t *testing.T) {
	d := newDBServer(t)
	trackingSite(t, d)
	cfg, _ := pgxpool.ParseConfig(mustEnv(t, "TEST_DATABASE_URL"))
	cfg.ConnConfig.RuntimeParams["search_path"] = currentSchema(t, d.store)
	pool, err := pgxpool.NewWithConfig(ctxBg(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	d.srv.Events.Store = store.New(pool)
	out := d.srv.Events.Exposure(ctxBg(), events.Exposure{Site: "acme", Visitor: "v1", Experiment: "hero-cta", Variant: "b"}, events.Credentials{Origin: acmeOrigin})
	if out.Accepted || out.Reason != events.DropStoreError {
		t.Fatalf("outcome %+v", out)
	}
	if code := d.beacon(t, "/v1/events/exposure", `{"site":"acme","v":"v1","experiment":"hero-cta","variant":"b"}`, acmeOrigin, ""); code != http.StatusAccepted {
		t.Fatalf("status during outage %d, want 202", code)
	}
}

func TestRecorderOutcomes(t *testing.T) {
	d := newDBServer(t)
	_, apiKey, _ := trackingSite(t, d)
	rec := d.srv.Events
	e := events.Exposure{Site: "acme", Visitor: "v1", Experiment: "hero-cta", Variant: "b"}
	cases := []struct {
		name  string
		e     events.Exposure
		creds events.Credentials
		want  string
	}{
		{"first", e, events.Credentials{Origin: acmeOrigin}, events.ReasonAccepted},
		{"dup", e, events.Credentials{Origin: acmeOrigin}, events.ReasonDuplicate},
		{"api key", events.Exposure{Site: "acme", Visitor: "v2", Experiment: "hero-cta", Variant: "b"}, events.Credentials{APIKey: apiKey}, events.ReasonAccepted},
		{"bad origin", e, events.Credentials{Origin: "https://evil.com"}, events.DropUnauthorized},
		{"unknown site", events.Exposure{Site: "nope", Visitor: "v1", Experiment: "hero-cta", Variant: "b"}, events.Credentials{Origin: acmeOrigin}, events.DropUnknownSite},
		{"unknown exp", events.Exposure{Site: "acme", Visitor: "v1", Experiment: "zzz", Variant: "b"}, events.Credentials{Origin: acmeOrigin}, events.DropUnknownExp},
		{"unknown variant", events.Exposure{Site: "acme", Visitor: "v1", Experiment: "hero-cta", Variant: "q"}, events.Credentials{Origin: acmeOrigin}, events.DropUnknownVariant},
		{"bad visitor", events.Exposure{Site: "acme", Visitor: strings.Repeat("x", 129), Experiment: "hero-cta", Variant: "b"}, events.Credentials{Origin: acmeOrigin}, events.DropBadVisitor},
	}
	for _, c := range cases {
		if out := rec.Exposure(ctxBg(), c.e, c.creds); out.Reason != c.want {
			t.Errorf("%s: reason %q, want %q", c.name, out.Reason, c.want)
		}
	}
}

func mustEnv(t *testing.T, k string) string {
	t.Helper()
	v := envOr(k)
	if v == "" {
		t.Skip(k + " not set")
	}
	return v
}
