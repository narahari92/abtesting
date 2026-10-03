package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"variantsvc/internal/experiment"
	"variantsvc/internal/payload"
)

type expResp struct {
	Experiment experimentView `json:"experiment"`
}

func heroRequest() createExperimentRequest {
	return createExperimentRequest{
		Key: "hero-cta", Name: "Hero CTA",
		Variants: []variantInput{
			{Key: "control", WeightBP: 5000, IsControl: true, Content: map[string]any{"headline": "Default"}},
			{Key: "b", WeightBP: 5000, Content: map[string]any{"headline": "Variant B"}},
		},
	}
}

func (d *dbServer) createExperiment(t *testing.T, apiKey string, req createExperimentRequest) experimentView {
	t.Helper()
	var resp expResp
	if res := d.call(t, "POST", "/v1/admin/experiments", apiKey, req, &resp); res.StatusCode != http.StatusCreated {
		t.Fatalf("create experiment: %d", res.StatusCode)
	}
	return resp.Experiment
}

func (d *dbServer) patch(t *testing.T, apiKey, key string, req patchExperimentRequest) (*http.Response, expResp, errorResponse) {
	t.Helper()
	var raw map[string]any
	res := d.call(t, "PATCH", "/v1/admin/experiments/"+key, apiKey, req, &raw)
	var out expResp
	var er errorResponse
	if msg, ok := raw["error"].(string); ok {
		er.Error = msg
	} else if res.StatusCode == 200 {
		// re-fetch typed
		d.call(t, "GET", "/v1/admin/experiments/"+key, apiKey, nil, &out)
	}
	return res, out, er
}

func TestCreateExperiment(t *testing.T) {
	d := newDBServer(t)
	site, apiKey := d.createSite(t, "acme")
	e := d.createExperiment(t, apiKey, heroRequest())
	if e.Status != experiment.StatusDraft || len(e.Seed) != 32 || e.HashVersion != 1 || e.CoverageBP != 10000 || len(e.Variants) != 2 {
		t.Fatalf("created: %+v", e)
	}
	if e.Ranges[0] != [2]int{0, 5000} || e.Ranges[1] != [2]int{5000, 10000} || e.Servable {
		t.Errorf("ranges %v servable %v", e.Ranges, e.Servable)
	}
	if e.Variants[0].Source != experiment.SourceManual || !e.Variants[0].Approved || e.Variants[1].Position != 1 {
		t.Errorf("variant defaults: %+v", e.Variants)
	}
	var st struct{ Site experiment.Site }
	d.call(t, "GET", "/v1/admin/site", apiKey, nil, &st)
	if st.Site.PayloadVersion != site.PayloadVersion+1 {
		t.Errorf("create must bump payload_version: %d -> %d", site.PayloadVersion, st.Site.PayloadVersion)
	}

	bad := heroRequest()
	bad.Key = "second"
	bad.Variants[1].WeightBP = 4000
	var er errorResponse
	if res := d.call(t, "POST", "/v1/admin/experiments", apiKey, bad, &er); res.StatusCode != http.StatusBadRequest || !strings.Contains(er.Error, "sum to 9000") {
		t.Errorf("weights: %d %q", res.StatusCode, er.Error)
	}
	if res := d.call(t, "POST", "/v1/admin/experiments", apiKey, heroRequest(), nil); res.StatusCode != http.StatusConflict {
		t.Errorf("duplicate key: %d", res.StatusCode)
	}
	cov := -5
	bad = heroRequest()
	bad.Key = "third"
	bad.CoverageBP = &cov
	if res := d.call(t, "POST", "/v1/admin/experiments", apiKey, bad, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad coverage: %d", res.StatusCode)
	}

	var list struct{ Experiments []experimentView }
	d.call(t, "GET", "/v1/admin/experiments", apiKey, nil, &list)
	if len(list.Experiments) != 1 || list.Experiments[0].Key != "hero-cta" {
		t.Errorf("list: %+v", list)
	}
	var one expResp
	if res := d.call(t, "GET", "/v1/admin/experiments/hero-cta", apiKey, nil, &one); res.StatusCode != 200 || one.Experiment.Seed != e.Seed {
		t.Errorf("detail: %d", res.StatusCode)
	}
	if res := d.call(t, "GET", "/v1/admin/experiments/nope", apiKey, nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("missing: %d", res.StatusCode)
	}
}

func TestStatusMachine(t *testing.T) {
	d := newDBServer(t)
	_, apiKey := d.createSite(t, "acme")
	d.createExperiment(t, apiKey, heroRequest())
	status := func(s experiment.Status) patchExperimentRequest { return patchExperimentRequest{Status: &s} }

	steps := []struct {
		to   experiment.Status
		want int
	}{
		{experiment.StatusPaused, http.StatusConflict},  // draft → paused
		{experiment.StatusRunning, http.StatusOK},       // draft → running
		{experiment.StatusDraft, http.StatusConflict},   // running → draft
		{experiment.StatusPaused, http.StatusOK},        // running → paused
		{experiment.StatusRunning, http.StatusOK},       // paused → running
		{experiment.StatusArchived, http.StatusOK},      // running → archived
		{experiment.StatusRunning, http.StatusConflict}, // archived is terminal
	}
	for i, st := range steps {
		res, _, er := d.patch(t, apiKey, "hero-cta", status(st.to))
		if res.StatusCode != st.want {
			t.Fatalf("step %d → %s: %d (%s), want %d", i, st.to, res.StatusCode, er.Error, st.want)
		}
	}
	bogus := experiment.Status("live")
	if res, _, _ := d.patch(t, apiKey, "hero-cta", patchExperimentRequest{Status: &bogus}); res.StatusCode != http.StatusConflict {
		t.Errorf("bogus status on archived: %d", res.StatusCode)
	}
}

func TestImmutabilityRules(t *testing.T) {
	d := newDBServer(t)
	_, apiKey := d.createSite(t, "acme")
	d.createExperiment(t, apiKey, heroRequest())

	// Draft: variants and coverage are free to change.
	cov := 2000
	res, e, er := d.patch(t, apiKey, "hero-cta", patchExperimentRequest{
		CoverageBP: &cov,
		Variants: []variantInput{
			{Key: "control", WeightBP: 9000, IsControl: true},
			{Key: "b", WeightBP: 1000},
		},
	})
	if res.StatusCode != 200 || e.Experiment.CoverageBP != 2000 || e.Experiment.Variants[0].WeightBP != 9000 {
		t.Fatalf("draft edit: %d %s", res.StatusCode, er.Error)
	}
	running := experiment.StatusRunning
	if res, _, er := d.patch(t, apiKey, "hero-cta", patchExperimentRequest{Status: &running}); res.StatusCode != 200 {
		t.Fatalf("start: %d %s", res.StatusCode, er.Error)
	}

	// Running: variants rejected with an explanation that mentions cloning.
	res, _, er = d.patch(t, apiKey, "hero-cta", patchExperimentRequest{Variants: []variantInput{{Key: "control", WeightBP: 10000, IsControl: true}}})
	if res.StatusCode != http.StatusConflict || !strings.Contains(er.Error, "new seed") {
		t.Errorf("variants while running: %d %q", res.StatusCode, er.Error)
	}
	// Coverage down rejected, up accepted.
	down := 1000
	if res, _, er := d.patch(t, apiKey, "hero-cta", patchExperimentRequest{CoverageBP: &down}); res.StatusCode != http.StatusConflict || !strings.Contains(er.Error, "only increase") {
		t.Errorf("coverage down: %d %q", res.StatusCode, er.Error)
	}
	up := 5000
	if res, e, _ := d.patch(t, apiKey, "hero-cta", patchExperimentRequest{CoverageBP: &up}); res.StatusCode != 200 || e.Experiment.CoverageBP != 5000 {
		t.Errorf("coverage up: %d", res.StatusCode)
	}
	// Name and description stay editable while running.
	if res, e, _ := d.patch(t, apiKey, "hero-cta", patchExperimentRequest{Name: ptr("Renamed"), Description: ptr("desc")}); res.StatusCode != 200 || e.Experiment.Name != "Renamed" {
		t.Errorf("rename: %d", res.StatusCode)
	}
	// Archived: everything rejected.
	archived := experiment.StatusArchived
	d.patch(t, apiKey, "hero-cta", patchExperimentRequest{Status: &archived})
	if res, _, er := d.patch(t, apiKey, "hero-cta", patchExperimentRequest{Name: ptr("x")}); res.StatusCode != http.StatusConflict || !strings.Contains(er.Error, "immutable") {
		t.Errorf("archived edit: %d %q", res.StatusCode, er.Error)
	}
}

func TestStartRequiresApprovedVariants(t *testing.T) {
	d := newDBServer(t)
	_, apiKey := d.createSite(t, "acme")
	e := d.createExperiment(t, apiKey, heroRequest())
	// Simulate an unapproved LLM draft directly in the store.
	site, err := d.store.GetSite(ctxBg(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	full, err := d.store.GetExperiment(ctxBg(), site.ID, e.Key)
	if err != nil {
		t.Fatal(err)
	}
	full.Variants[1].Approved = false
	full.Variants[1].Source = experiment.SourceLLM
	if _, err := d.store.SaveExperiment(ctxBg(), site.ID, full); err != nil {
		t.Fatal(err)
	}
	running := experiment.StatusRunning
	res, _, er := d.patch(t, apiKey, "hero-cta", patchExperimentRequest{Status: &running})
	if res.StatusCode != http.StatusConflict || !strings.Contains(er.Error, "unapproved") {
		t.Fatalf("start with unapproved variant: %d %q", res.StatusCode, er.Error)
	}
}

func TestRunningExperimentReachesPayloadAndBumpsVersion(t *testing.T) {
	d := newDBServer(t)
	_, apiKey := d.createSite(t, "acme")
	d.createExperiment(t, apiKey, heroRequest())
	var before payload.Payload
	d.call(t, "GET", "/v1/sites/acme/payload.json", "", nil, &before)
	if len(before.Experiments) != 0 {
		t.Fatal("draft must not be in payload")
	}
	running := experiment.StatusRunning
	if res, _, er := d.patch(t, apiKey, "hero-cta", patchExperimentRequest{Status: &running}); res.StatusCode != 200 {
		t.Fatalf("start: %d %s", res.StatusCode, er.Error)
	}
	var after payload.Payload
	res, _ := http.Get(d.ts.URL + "/v1/sites/acme/payload.json")
	etag := res.Header.Get("ETag")
	res.Body.Close()
	d.call(t, "GET", "/v1/sites/acme/payload.json", "", nil, &after)
	if len(after.Experiments) != 1 || after.Experiments[0].Key != "hero-cta" || after.Version <= before.Version {
		t.Fatalf("payload after start: %+v (before version %d)", after, before.Version)
	}
	if etag != after.ETag() {
		t.Errorf("ETag %s != %s", etag, after.ETag())
	}
	paused := experiment.StatusPaused
	d.patch(t, apiKey, "hero-cta", patchExperimentRequest{Status: &paused})
	var afterPause payload.Payload
	d.call(t, "GET", "/v1/sites/acme/payload.json", "", nil, &afterPause)
	if len(afterPause.Experiments) != 0 || afterPause.Version <= after.Version {
		t.Errorf("paused experiment must leave payload with a new version: %+v", afterPause)
	}
}

func TestTenantIsolationInAdminAPI(t *testing.T) {
	d := newDBServer(t)
	_, keyA := d.createSite(t, "site-a")
	_, keyB := d.createSite(t, "site-b")
	d.createExperiment(t, keyA, heroRequest())
	other := heroRequest()
	other.Key = "b-only"
	d.createExperiment(t, keyB, other)

	if res := d.call(t, "GET", "/v1/admin/experiments/hero-cta", keyB, nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("B reading A's experiment: %d, want 404", res.StatusCode)
	}
	running := experiment.StatusRunning
	if res, _, _ := d.patch(t, keyB, "hero-cta", patchExperimentRequest{Status: &running}); res.StatusCode != http.StatusNotFound {
		t.Errorf("B mutating A's experiment: %d, want 404", res.StatusCode)
	}
	var list struct{ Experiments []experimentView }
	d.call(t, "GET", "/v1/admin/experiments", keyB, nil, &list)
	if len(list.Experiments) != 1 || list.Experiments[0].Key != "b-only" {
		t.Errorf("B's list: %+v", list.Experiments)
	}
	// Same experiment key on both sites is allowed and independent.
	if res := d.call(t, "POST", "/v1/admin/experiments", keyB, heroRequest(), nil); res.StatusCode != http.StatusCreated {
		t.Errorf("B creating hero-cta: %d", res.StatusCode)
	}
	var pa, pb payload.Payload
	d.patch(t, keyA, "hero-cta", patchExperimentRequest{Status: &running})
	d.call(t, "GET", "/v1/sites/site-a/payload.json", "", nil, &pa)
	d.call(t, "GET", "/v1/sites/site-b/payload.json", "", nil, &pb)
	if len(pa.Experiments) != 1 || len(pb.Experiments) != 0 {
		t.Errorf("payload isolation: a=%d b=%d", len(pa.Experiments), len(pb.Experiments))
	}
}
