package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"variantsvc/internal/configcache"
	"variantsvc/internal/experiment"
	"variantsvc/internal/payload"
	"variantsvc/internal/store"
)

// TestRefresherPicksUpDirectDBChanges writes to the database behind the
// API's back (as another replica would) and waits for the polling
// refresher to publish it with a new version and ETag.
func TestRefresherPicksUpDirectDBChanges(t *testing.T) {
	d := newDBServer(t)
	_, apiKey := d.createSite(t, "acme")
	d.createExperiment(t, apiKey, heroRequest())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.srv.Cache.Run(ctx, d.srv.Source, 20*time.Millisecond, d.srv.Log)

	res, _ := http.Get(d.ts.URL + "/v1/sites/acme/payload.json")
	etagBefore := res.Header.Get("ETag")
	res.Body.Close()

	site, _ := d.store.GetSite(ctxBg(), "acme")
	e, _ := d.store.GetExperiment(ctxBg(), site.ID, "hero-cta")
	e.Status = experiment.StatusRunning
	if _, err := d.store.SaveExperiment(ctxBg(), site.ID, e); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		res, _ := http.Get(d.ts.URL + "/v1/sites/acme/payload.json")
		var p payload.Payload
		json.NewDecoder(res.Body).Decode(&p)
		res.Body.Close()
		if len(p.Experiments) == 1 && res.Header.Get("ETag") != etagBefore && res.Header.Get("ETag") == p.ETag() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("refresher did not publish the change within 3 s")
}

// TestRefresherSurvivesDBOutage closes the pool under the source and
// checks the snapshot keeps serving; readiness reports degraded, not down.
func TestRefresherSurvivesDBOutage(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	d := newDBServer(t)
	_, apiKey := d.createSite(t, "acme")
	d.createExperiment(t, apiKey, heroRequest())
	running := experiment.StatusRunning
	d.patch(t, apiKey, "hero-cta", patchExperimentRequest{Status: &running})

	// A second pool to the same schema that we can close independently.
	cfg, _ := pgxpool.ParseConfig(url)
	cfg.ConnConfig.RuntimeParams["search_path"] = currentSchema(t, d.store)
	pool, err := pgxpool.NewWithConfig(ctxBg(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	flaky := store.New(pool)
	src := configcache.DBSource{Store: flaky}
	if err := d.srv.Cache.Refresh(ctxBg(), src, slog.Default()); err != nil {
		t.Fatal(err)
	}
	before := d.srv.Cache.Current()
	pool.Close()

	if err := d.srv.Cache.Refresh(ctxBg(), src, slog.Default()); err == nil {
		t.Fatal("refresh over a closed pool should fail")
	}
	if d.srv.Cache.Current() != before {
		t.Fatal("failed refresh must keep the previous snapshot")
	}
	var p payload.Payload
	if res := d.call(t, "GET", "/v1/sites/acme/payload.json", "", nil, &p); res.StatusCode != 200 || len(p.Experiments) != 1 {
		t.Fatalf("payload during outage: %d %+v", res.StatusCode, p)
	}
	var a assignResponse
	d.call(t, "GET", "/v1/assign?site=acme&v=visitor-1", "", nil, &a)
	if len(a.Assignments) != 1 {
		t.Fatalf("assign during outage: %+v", a)
	}

	// Readiness with an unreachable store: 200 and degraded.
	d.srv.Store = flaky
	var rr readyResponse
	if res := d.call(t, "GET", "/readyz", "", nil, &rr); res.StatusCode != 200 || rr.Status != "degraded" || rr.DBReachable == nil || *rr.DBReachable {
		t.Fatalf("readyz during outage: %d %+v", res.StatusCode, rr)
	}
	d.srv.Store = d.store
	if res := d.call(t, "GET", "/readyz", "", nil, &rr); res.StatusCode != 200 || rr.Status != "ok" || rr.DBReachable == nil || !*rr.DBReachable {
		t.Fatalf("readyz healthy: %d %+v", res.StatusCode, rr)
	}
}

func currentSchema(t *testing.T, s *store.Store) string {
	t.Helper()
	var schema string
	if err := s.Pool().QueryRow(ctxBg(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

// TestAssignStableAcrossRestart boots a second, independent server over
// the same database and compares assignments: nothing about a visitor's
// variant lives in process memory.
func TestAssignStableAcrossRestart(t *testing.T) {
	d := newDBServer(t)
	_, apiKey := d.createSite(t, "acme")
	req := heroRequest()
	req.Variants = append(req.Variants, variantInput{Key: "c", WeightBP: 2000})
	req.Variants[0].WeightBP = 4000
	req.Variants[1].WeightBP = 4000
	cov := 9000
	req.CoverageBP = &cov
	d.createExperiment(t, apiKey, req)
	running := experiment.StatusRunning
	d.patch(t, apiKey, "hero-cta", patchExperimentRequest{Status: &running})

	fresh := &configcache.Cache{}
	if err := fresh.Refresh(ctxBg(), configcache.DBSource{Store: d.store}, slog.Default()); err != nil {
		t.Fatal(err)
	}
	second := httptest.NewServer((&Server{Cache: fresh, Store: d.store, Log: d.srv.Log}).Handler())
	defer second.Close()

	for i := 0; i < 200; i++ {
		v := "visitor-" + string(rune('a'+i%26)) + string(rune('0'+i%10)) + string(rune('A'+i%7))
		var a, b assignResponse
		d.call(t, "GET", "/v1/assign?site=acme&v="+v, "", nil, &a)
		res, _ := http.Get(second.URL + "/v1/assign?site=acme&v=" + v)
		json.NewDecoder(res.Body).Decode(&b)
		res.Body.Close()
		if len(a.Assignments) != len(b.Assignments) || (len(a.Assignments) == 1 && a.Assignments[0].Variant != b.Assignments[0].Variant) {
			t.Fatalf("visitor %s: %+v vs %+v", v, a, b)
		}
	}
}
