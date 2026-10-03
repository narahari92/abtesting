package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"variantsvc/internal/events"
	"variantsvc/internal/results"
)

// seedEvents writes n exposures per variant through the recorder and
// converts the first k of each on the given goals.
func seedEvents(t *testing.T, d *dbServer, variant string, n, k int, goals ...string) {
	t.Helper()
	creds := events.Credentials{Origin: acmeOrigin}
	for i := 0; i < n; i++ {
		v := variant + "-visitor-" + itoa(i)
		if out := d.srv.Events.Exposure(ctxBg(), events.Exposure{Site: "acme", Visitor: v, Experiment: "hero-cta", Variant: variant}, creds); !out.Accepted {
			t.Fatalf("exposure dropped: %s", out.Reason)
		}
		if i < k {
			for _, g := range goals {
				val := 10.0
				d.srv.Events.Conversion(ctxBg(), events.Conversion{Site: "acme", Visitor: v, Experiment: "hero-cta", Goal: g, Value: &val}, creds)
			}
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestResultsEndpoint(t *testing.T) {
	d := newDBServer(t)
	_, apiKey, _ := trackingSite(t, d)
	seedEvents(t, d, "control", 120, 12, "signup")
	seedEvents(t, d, "b", 110, 22, "signup", "checkout")
	// Conversions from visitors who were never exposed are unattributed.
	d.srv.Events.Conversion(ctxBg(), events.Conversion{Site: "acme", Visitor: "stranger-1", Experiment: "hero-cta", Goal: "signup"}, events.Credentials{Origin: acmeOrigin})
	d.srv.Events.Conversion(ctxBg(), events.Conversion{Site: "acme", Visitor: "stranger-2", Experiment: "hero-cta", Goal: "signup"}, events.Credentials{Origin: acmeOrigin})

	var r results.Report
	res := d.call(t, "GET", "/v1/admin/experiments/hero-cta/results", apiKey, nil, &r)
	if res.StatusCode != 200 || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d cache %q", res.StatusCode, res.Header.Get("Cache-Control"))
	}
	if r.Experiment != "hero-cta" || r.Control != "control" || r.Status != "running" || r.TotalExposures != 230 || r.UnattributedConversions != 2 {
		t.Fatalf("header: %+v", r)
	}
	if strings.Join(r.Goals, ",") != "checkout,signup" {
		t.Errorf("goals %v", r.Goals)
	}
	ctl, b := r.Variants[0], r.Variants[1]
	if ctl.Key != "control" || ctl.Exposures != 120 || ctl.Conversions != 12 || ctl.Rate != 0.1 || !ctl.IsControl {
		t.Errorf("control: %+v", ctl)
	}
	if b.Exposures != 110 || b.Conversions != 22 || b.Rate != 0.2 || b.ByGoal["signup"] != 22 || b.ByGoal["checkout"] != 22 || b.ValueByGoal["checkout"] != 220 {
		t.Errorf("b: %+v", b)
	}
	if r.TotalConversions != 34 {
		t.Errorf("total conversions %d, want 34", r.TotalConversions)
	}
	joined := strings.Join(r.Notes, " ")
	if !strings.Contains(joined, "2 conversion(s) had no matching exposure") {
		t.Errorf("notes: %q", joined)
	}

	// Goal filter: checkout only exists for b.
	var rc results.Report
	d.call(t, "GET", "/v1/admin/experiments/hero-cta/results?goal=checkout", apiKey, nil, &rc)
	if rc.Goal != "checkout" || rc.Variants[0].Conversions != 0 || rc.Variants[1].Conversions != 22 || rc.UnattributedConversions != 0 {
		t.Errorf("goal filter: %+v", rc.Variants)
	}
	if res := d.call(t, "GET", "/v1/admin/experiments/hero-cta/results?goal=Bad%20Goal", apiKey, nil, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad goal: %d", res.StatusCode)
	}
}

func TestResultsIsolationAndMissing(t *testing.T) {
	d := newDBServer(t)
	_, apiKey, _ := trackingSite(t, d)
	_, otherKey := d.createSite(t, "other", "https://other.example")
	if res := d.call(t, "GET", "/v1/admin/experiments/hero-cta/results", otherKey, nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("other tenant: %d, want 404", res.StatusCode)
	}
	if res := d.call(t, "GET", "/v1/admin/experiments/nope/results", apiKey, nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("missing experiment: %d", res.StatusCode)
	}
	var r results.Report
	if res := d.call(t, "GET", "/v1/admin/experiments/hero-cta/results", apiKey, nil, &r); res.StatusCode != 200 || r.TotalExposures != 0 || len(r.Variants) != 2 {
		t.Errorf("empty results: %d %+v", res.StatusCode, r)
	}
	if !strings.Contains(strings.Join(r.Notes, " "), "No exposures") {
		t.Error("empty note missing")
	}
}
