package results

import (
	"strings"
	"testing"
	"time"

	"variantsvc/internal/experiment"
	"variantsvc/internal/store"
)

func exp3() *experiment.Experiment {
	return &experiment.Experiment{Key: "hero", Status: experiment.StatusRunning, Variants: []experiment.Variant{
		{Key: "a", WeightBP: 5000, IsControl: true, Position: 0},
		{Key: "b", WeightBP: 5000, Position: 1},
		{Key: "ghost", WeightBP: 0, Position: 2},
	}}
}

func TestBuildReport(t *testing.T) {
	counts := map[string]*store.VariantCounts{
		"a":       {VariantKey: "a", Exposures: 5120, Converted: 256, ByGoal: map[string]int{"signup": 256}},
		"b":       {VariantKey: "b", Exposures: 5088, Converted: 305, ByGoal: map[string]int{"signup": 300, "checkout": 40}, ValueByGoal: map[string]float64{"checkout": 1960}},
		"removed": {VariantKey: "removed", Exposures: 9},
	}
	r := Build(exp3(), counts, 12, []string{"checkout", "signup"}, "", time.Unix(0, 0))
	if r.Control != "a" || len(r.Variants) != 3 || r.TotalExposures != 10208 || r.TotalConversions != 561 || r.UnattributedConversions != 12 {
		t.Fatalf("report header: %+v", r)
	}
	a, b, g := r.Variants[0], r.Variants[1], r.Variants[2]
	if a.Rate != 0.05 || a.Conversions != 256 || !a.IsControl {
		t.Errorf("control row: %+v", a)
	}
	if b.Exposures != 5088 || b.ByGoal["checkout"] != 40 || b.ValueByGoal["checkout"] != 1960 {
		t.Errorf("b row: %+v", b)
	}
	if g.Exposures != 0 || g.Rate != 0 || g.ByGoal == nil || g.ValueByGoal == nil {
		t.Errorf("empty arm: %+v", g)
	}
	joined := strings.Join(r.Notes, "\n")
	if !strings.Contains(joined, "12 conversion(s) had no matching exposure") || strings.Contains(joined, "No exposures") {
		t.Errorf("notes: %q", joined)
	}
}

func TestBuildEmptyAndGoalFilter(t *testing.T) {
	e := exp3()
	e.Status = experiment.StatusDraft
	r := Build(e, nil, 0, nil, "signup", time.Now())
	if r.TotalExposures != 0 || len(r.Variants) != 3 || r.Goal != "signup" || r.Goals == nil {
		t.Fatalf("empty report: %+v", r)
	}
	if len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "No exposures") {
		t.Errorf("notes for a draft with no data: %q", r.Notes)
	}
}
