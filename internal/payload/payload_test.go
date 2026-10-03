package payload

import (
	"bytes"
	"encoding/json"
	"testing"

	"variantsvc/internal/experiment"
)

func fixtureExperiments() []experiment.Experiment {
	mk := func(key string, status experiment.Status, approved bool, coverage int) experiment.Experiment {
		return experiment.Experiment{
			Key: key, Status: status, Seed: "seed-" + key, HashVersion: 1, CoverageBP: coverage, URLPath: "/" + key + "/index.html",
			Variants: []experiment.Variant{
				{Key: "b", WeightBP: 5000, Source: experiment.SourceManual, Approved: approved, Position: 1, Content: map[string]any{"headline": "Ship faster today"}},
				{Key: "control", WeightBP: 5000, IsControl: true, Source: experiment.SourceManual, Approved: true, Position: 0, Content: map[string]any{"headline": "Default headline"}},
			},
		}
	}
	return []experiment.Experiment{
		mk("zeta", experiment.StatusRunning, true, 10000),
		mk("hero-cta", experiment.StatusRunning, true, 9000),
		mk("draft-one", experiment.StatusDraft, true, 10000),
		mk("paused-one", experiment.StatusPaused, true, 10000),
		mk("unapproved", experiment.StatusRunning, false, 10000),
	}
}

func TestCompile(t *testing.T) {
	p, skipped := Compile("acme", 42, fixtureExperiments())
	if len(skipped) != 1 || skipped[0] != "unapproved" {
		t.Fatalf("skipped = %v", skipped)
	}
	got, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"site":"acme","version":42,"experiments":[` +
		`{"key":"hero-cta","url_path":"/hero-cta","seed":"seed-hero-cta","hash_version":1,"ranges":[[0,4500],[5000,9500]],"variants":[` +
		`{"key":"control","content":{"headline":"Default headline"}},{"key":"b","content":{"headline":"Ship faster today"}}]},` +
		`{"key":"zeta","url_path":"/zeta","seed":"seed-zeta","hash_version":1,"ranges":[[0,5000],[5000,10000]],"variants":[` +
		`{"key":"control","content":{"headline":"Default headline"}},{"key":"b","content":{"headline":"Ship faster today"}}]}]}`
	if string(got) != want {
		t.Fatalf("payload mismatch\n got: %s\nwant: %s", got, want)
	}
	if p.ETag() != `"42"` {
		t.Errorf("ETag = %s", p.ETag())
	}
}

func TestCompileIsDeterministic(t *testing.T) {
	a, _ := Compile("acme", 1, fixtureExperiments())
	exps := fixtureExperiments()
	exps[0], exps[1] = exps[1], exps[0]
	exps[0].Variants[0], exps[0].Variants[1] = exps[0].Variants[1], exps[0].Variants[0]
	b, _ := Compile("acme", 1, exps)
	ab, _ := a.Marshal()
	bb, _ := b.Marshal()
	if !bytes.Equal(ab, bb) {
		t.Fatalf("input order changed output:\n%s\n%s", ab, bb)
	}
}

func TestEmptyAndNoExperiments(t *testing.T) {
	for _, p := range []*Payload{Empty("ghost"), first(Compile("ghost", 7, nil))} {
		b, _ := p.Marshal()
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		if exps, ok := m["experiments"].([]any); !ok || len(exps) != 0 {
			t.Errorf("experiments must be an empty array, got %s", b)
		}
	}
	if Empty("x").ETag() != `"0"` {
		t.Error("empty payload ETag should be \"0\"")
	}
}

func first(p *Payload, _ []string) *Payload { return p }

func TestNilContentBecomesEmptyObject(t *testing.T) {
	exps := fixtureExperiments()[:1]
	exps[0].Variants[0].Content = nil
	p, _ := Compile("acme", 1, exps)
	b, _ := p.Marshal()
	if !bytes.Contains(b, []byte(`"content":{}`)) {
		t.Fatalf("nil content must serialize as {}: %s", b)
	}
}

func TestEvaluate(t *testing.T) {
	p, _ := Compile("acme", 1, fixtureExperiments())
	all := p.Evaluate("visitor-8841", nil, "")
	if len(all) == 0 || len(all) > 2 {
		t.Fatalf("expected 1..2 assignments, got %v", all)
	}
	only := p.Evaluate("visitor-8841", []string{"zeta", "nope"}, "")
	if len(only) != 1 || only[0].Experiment != "zeta" {
		t.Fatalf("filter failed: %v", only)
	}
	if len(p.Evaluate("", nil, "")) != 0 {
		t.Error("invalid visitor must get no assignments")
	}
	// Page filter: only the experiment on that page, with path normalisation.
	for _, page := range []string{"/zeta", "/zeta/", "/zeta/index.html"} {
		if got := p.Evaluate("visitor-8841", nil, page); len(got) != 1 || got[0].Experiment != "zeta" {
			t.Errorf("page %q: %v", page, got)
		}
	}
	if got := p.Evaluate("visitor-8841", nil, "/other"); len(got) != 0 {
		t.Errorf("unrelated page: %v", got)
	}
	if got := p.Evaluate("visitor-8841", nil, "not-a-path"); len(got) != 0 {
		t.Errorf("invalid page must match nothing: %v", got)
	}
	again := p.Evaluate("visitor-8841", nil, "")
	if len(again) != len(all) || again[0].Variant != all[0].Variant {
		t.Error("evaluation is not stable")
	}
	if p.Find("zeta") == nil || p.Find("missing") != nil {
		t.Error("Find")
	}
}
