package experiment

import (
	"strings"
	"testing"
)

func valid() Experiment {
	return Experiment{
		Key: "hero-cta", Name: "Hero CTA", Status: StatusDraft, Seed: "3f9a1c", HashVersion: 1, CoverageBP: 10000,
		Variants: []Variant{
			{Key: "control", WeightBP: 5000, IsControl: true, Source: SourceManual, Approved: true, Position: 0},
			{Key: "b", WeightBP: 5000, Source: SourceManual, Approved: true, Position: 1},
		},
	}
}

func TestValidateAcceptsValid(t *testing.T) {
	e := valid()
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	single := valid()
	single.Variants = single.Variants[:1]
	single.Variants[0].WeightBP = 10000
	if err := single.Validate(); err != nil {
		t.Fatalf("control-only draft should be valid: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]struct {
		mutate func(*Experiment)
		want   string
	}{
		"bad key":           {func(e *Experiment) { e.Key = "Hero CTA" }, "key"},
		"hyphen start":      {func(e *Experiment) { e.Key = "-x" }, "key"},
		"long name":         {func(e *Experiment) { e.Name = strings.Repeat("n", MaxNameLen+1) }, "name longer"},
		"bad status":        {func(e *Experiment) { e.Status = "live" }, "status"},
		"empty seed":        {func(e *Experiment) { e.Seed = "" }, "seed"},
		"hash version":      {func(e *Experiment) { e.HashVersion = 2 }, "hash_version"},
		"coverage high":     {func(e *Experiment) { e.CoverageBP = 10001 }, "coverage_bp"},
		"coverage negative": {func(e *Experiment) { e.CoverageBP = -1 }, "coverage_bp"},
		"no variants":       {func(e *Experiment) { e.Variants = nil }, "at least one variant"},
		"weights sum":       {func(e *Experiment) { e.Variants[1].WeightBP = 4000 }, "sum to 9000"},
		"two controls":      {func(e *Experiment) { e.Variants[1].IsControl = true }, "exactly one control"},
		"no control":        {func(e *Experiment) { e.Variants[0].IsControl = false }, "exactly one control"},
		"dup variant key":   {func(e *Experiment) { e.Variants[1].Key = "control" }, "duplicated"},
		"bad variant key":   {func(e *Experiment) { e.Variants[1].Key = "B!" }, "variant key"},
		"bad source":        {func(e *Experiment) { e.Variants[1].Source = "robot" }, "source"},
	}
	for name, c := range cases {
		e := valid()
		c.mutate(&e)
		err := e.Validate()
		if err == nil {
			t.Errorf("%s: expected error", name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not mention %q", name, err, c.want)
		}
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	e := valid()
	e.Key = "Bad"
	e.CoverageBP = 20000
	err := e.Validate()
	if err == nil || !strings.Contains(err.Error(), "key") || !strings.Contains(err.Error(), "coverage_bp") {
		t.Fatalf("expected both problems, got %v", err)
	}
}

func TestServable(t *testing.T) {
	e := valid()
	if e.Servable() {
		t.Error("draft must not be servable")
	}
	e.Status = StatusRunning
	if !e.Servable() {
		t.Error("running, valid, approved must be servable")
	}
	e.Variants[1].Approved = false
	if e.Servable() {
		t.Error("unapproved variant must block serving")
	}
	e.Variants[1].Approved = true
	e.Variants[1].WeightBP = 1
	if e.Servable() {
		t.Error("invalid experiment must not be servable")
	}
}

func TestValidKey(t *testing.T) {
	for _, k := range []string{"a", "hero-cta", "x1-2-3", strings.Repeat("a", 64)} {
		if !ValidKey(k) {
			t.Errorf("%q should be valid", k)
		}
	}
	for _, k := range []string{"", "-a", "A", "a_b", "a b", "a.b", strings.Repeat("a", 65)} {
		if ValidKey(k) {
			t.Errorf("%q should be invalid", k)
		}
	}
}
